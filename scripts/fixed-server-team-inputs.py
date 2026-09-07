#!/usr/bin/env python3
"""生成待确认的订单团队输入；不批准、不创建 Run、不启动 Worker。"""

import argparse
import copy
import datetime
import hashlib
import importlib.util
import json
from pathlib import Path
import types


def load_t2():
    spec = importlib.util.spec_from_file_location("t2_task", Path(__file__).with_name("fixed-server-t2-task.py"))
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


t2 = load_t2()
canonical = t2.canonical_bytes
digest = lambda value: "sha256:" + hashlib.sha256(canonical(value)).hexdigest()
VERSION = "bounded-team-inputs/v1"
PATHS = {"service": ["quote_api.py"], "client": ["quote_client.py"], "integration": ["quote_api.py", "quote_client.py", "quote_delivery.json"]}
CONTRACT = (
    "订单报价共享契约：POST http://127.0.0.1:PORT/quote（PORT 为实际绑定端口），Content-Type application/json，body 恰含 items。"
    "items 是非空 list，每项恰含 unit_price_cents 和 quantity；单价非负 int，数量正 int，bool/float 非法。"
    "subtotal_cents 为乘积和，>=5000 时 shipping_cents 为0，否则500；total_cents 为两者之和。"
    "报价恰含这三个 int 字段，支持大整数。非法 JSON 返回400和{\"error\":\"invalid-json\"}；"
    "非法订单返回422和{\"error\":\"invalid-order\"}；未知路径返回404和{\"error\":\"not-found\"}。"
    "所有响应为 application/json，成功或失败均不修改调用者的 items。"
)
OBJECTIVES = {
    "service": "实现 quote_api.py，导出 create_server(host, port)，只接受 host=127.0.0.1，支持 port=0。返回已绑定但未启动 serve_forever 的标准库 HTTPServer，由调用者启动和关闭。导入模块不得启动服务。实现共享 HTTP/报价契约。",
    "client": "实现 quote_client.py，导出 quote_order(base_url, items)。base_url 只接受 http://127.0.0.1:PORT（无用户信息、路径、query、fragment）；空 userinfo 也必须在联网前拒绝。发送一次 POST http://127.0.0.1:PORT/quote（使用 base_url 的实际端口），消费服务的200报价响应。200 JSON 必须恰含共享契约的三个报价字段，值须为 int 而非 bool/float；其他结构属于协议失败。只校验响应结构，不重算价格；禁止本地重算、伪造响应或跟随重定向。422或连接/协议失败抛 ValueError，不修改输入；单次网络超时最多3秒。导入模块无副作用。",
    "integration": "验证并在必要时修复已接纳的 quote_api.py 与 quote_client.py：由客户端实际向同一个服务发起 HTTP 调用，通过共享契约和独立集成 oracle。保留接口与预算，不扩展功能。代码正确时不要制造代码修改；仍须交付 quote_delivery.json，恰含 version=order-quote-delivery/v1、apiSha256 和 clientSha256（对应最终两个文件原始 bytes 的小写 SHA-256 hex，无前缀）、apiEntryPoint=quote_api.create_server、clientEntryPoint=quote_client.quote_order、sampleItems=[{\"unit_price_cents\":1200,\"quantity\":2}]、sampleQuote={\"subtotal_cents\":2400,\"shipping_cents\":500,\"total_cents\":2900}。此清单用于交付接口与示例，不是测试通过声明或权威证据；独立 verifier 会重算摘要并实际执行 HTTP 验收。",
}


def node_ids(namespace, goal_id, proposal_id, node_id):
    identity = {"version": VERSION, "namespace": namespace, "goalId": goal_id, "proposalId": proposal_id, "nodeId": node_id}
    value = digest(identity)[7:]
    return "team-task-" + value, "team-run-" + value


def build(args):
    for value in (args.goal_id, args.proposal_id, args.request_id):
        if not t2.ID.fullmatch(value):
            raise ValueError("invalid-team-id")
    stamp = datetime.datetime.fromisoformat(args.deadline.replace("Z", "+00:00"))
    if stamp.utcoffset() != datetime.timedelta(0) or stamp.isoformat().replace("+00:00", "Z") != args.deadline:
        raise ValueError("canonical-utc-deadline-required")
    namespace = {"tenantNamespace": "local", "controlPlaneId": "default", "authorityScopeId": args.repository}
    spec = {"authorityNamespaceId": namespace, "goalId": args.goal_id, "revision": 1, "previousDigest": "",
            "projectId": "order-quote", "repository": args.repository, "title": "订单报价 API 与客户端", "description": CONTRACT}
    estimate = {"runs": 1, "attempts": 1, "wallTimeSeconds": 600, "computeUnits": 0, "tokens": 0, "artifactBytes": 8388608}
    proposal = {"authorityNamespaceId": namespace, "proposalId": args.proposal_id, "goalId": args.goal_id,
                "projectId": spec["projectId"], "repository": args.repository, "goalSpecRevision": 1, "goalSpecDigest": digest(spec),
                "basedOnPlanRevision": 0, "basedOnPlanDigest": "", "plannerIdentity": "order-quote-reference-not-approval",
                "nodes": [{"nodeId": node, "executorKind": "implement", "title": node, "repository": args.repository,
                           "paths": paths, "sideEffectClasses": ["workspace-write"], "estimate": copy.deepcopy(estimate)} for node, paths in PATHS.items()],
                "edges": [{"from": node, "to": "integration", "kind": "depends-on"} for node in ("service", "client")], "supersessions": []}
    oracle = Path(args.repository)/"scripts/order-quote-team-oracle.py"
    if oracle.is_symlink() or not oracle.is_file():
        raise ValueError("fixed-team-oracle-required")
    oracle_sha = hashlib.sha256(oracle.read_bytes()).hexdigest()
    command = (
        "import hashlib,pathlib,sys; p=pathlib.Path(sys.argv[1]); "
        "data=p.read_bytes() if not p.is_symlink() else b''; "
        "hashlib.sha256(data).hexdigest()==sys.argv[2] or sys.exit('oracle-drift'); "
        "sys.argv=[str(p)]+sys.argv[3:]; "
        "exec(compile(data,str(p),'exec'),{'__name__':'__main__','__file__':str(p)})"
    )
    nodes = []
    for node, paths in PATHS.items():
        task_id, run_id = node_ids(namespace, args.goal_id, args.proposal_id, node)
        task, policy = t2.build(types.SimpleNamespace(repository=args.repository, base_ref=args.base_ref, doctor=args.doctor,
                            model=args.model, task_id=task_id, run_id=run_id, scenario="marker", long_verify=False))
        task["metadata"]["title"] = "订单团队：" + node
        task["admission"] = {"status": "executable"}
        task["work"] = {"objective": OBJECTIVES[node]+"最终回复必须是一个 WorkerResult JSON 对象。",
                        "constraints": ["只修改已批准 scope；不提交、推送、创建 Git 引用；不访问外部网络；不启动子 Agent。"],
                        "context": [CONTRACT], "nonGoals": ["不部署、不发布、不扩展框架和第三方依赖。"]}
        task["scope"].update(allowPaths=paths, maxChangedFiles=len(paths), maxDiffBytes=60000)
        argv = ["/usr/bin/python3", "-I", "-B", "-c", command, str(oracle), oracle_sha]
        if node != "client": argv += ["--api", "quote_api.py"]
        if node != "service": argv += ["--client", "quote_client.py"]
        if node == "integration": argv += ["--delivery", "quote_delivery.json"]
        task["acceptance"] = {"allowNoChange": False, "commands": [{"id": "quote-team-"+node, "argv": argv,
                              "cwd": ".", "timeoutSeconds": 30, "maxLogBytes": 4000, "required": True, "baselinePolicy": "none"}]}
        task["deliverables"] = [{"id": "quote-"+str(index), "kind": "diagnostic" if path == "quote_delivery.json" else "code", "required": True, "pathGlob": path, "minimumCount": 1} for index,path in enumerate(paths)]
        nodes.append({"nodeId": node, "role": "integrate" if node == "integration" else "implement", "task": task, "policy": policy})
    limits = {"maxNodes": 3, "maxDepth": 2, "maxFanOut": 2, "maxConcurrentNodes": 2, "maxPlanRevisions": 1,
              "maxTotalRuns": 3, "maxTotalAttempts": 3, "maxWallTimeSeconds": 1800, "maxComputeUnits": 1, "maxTokens": 1000000, "maxArtifactBytes": 3*8388608}
    inputs = {"schemaVersion": VERSION, "spec": spec, "proposal": proposal, "limits": limits, "baseSha": args.base_ref, "nodes": nodes,
              "admissionPolicy": {"executorKinds": ["implement"], "repositories": [args.repository], "paths": PATHS["integration"], "sideEffectClasses": ["workspace-write"]}}
    return {"protocolRevision": "initial-team-approval/v1", "requestId": args.request_id, "inputs": inputs,
            "inputsDigest": digest(inputs), "expectedHead": "", "deadline": args.deadline}


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("repository", "base-ref", "doctor", "model", "goal-id", "proposal-id", "request-id", "deadline", "out"):
        parser.add_argument("--"+name, required=True)
    args = parser.parse_args()
    request = build(args)
    # A generated file is a proposal. Only a separately invoked authenticated
    # team-approve operation can authorize work from these exact bytes.
    with open(args.out, "x", encoding="utf-8") as output:
        output.write(canonical(request).decode()+"\n")
