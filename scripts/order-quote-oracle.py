#!/usr/bin/env python3
"""订单报价的独立业务 oracle；仅用于可信仓库，不提供恶意代码隔离。"""

import argparse
import copy
import importlib.util
import pathlib
import sys


def check(quote):
    cases = [
        ([{"unit_price_cents": 1200, "quantity": 2}], (2400, 500, 2900)),
        ([{"unit_price_cents": 4999, "quantity": 1}], (4999, 500, 5499)),
        ([{"unit_price_cents": 2500, "quantity": 2}], (5000, 0, 5000)),
        ([{"unit_price_cents": 2000, "quantity": 2},
          {"unit_price_cents": 1000, "quantity": 1}], (5000, 0, 5000)),
        ([{"unit_price_cents": 0, "quantity": 3}], (0, 500, 500)),
        ([{"unit_price_cents": 10**18 + 3, "quantity": 7}],
         (7 * (10**18 + 3), 0, 7 * (10**18 + 3))),
    ]
    keys = ("subtotal_cents", "shipping_cents", "total_cents")
    checks = 0
    for items, expected in cases:
        before = copy.deepcopy(items)
        result = quote(items)
        if type(result) is not dict or set(result) != set(keys):
            raise ValueError("result-shape")
        if any(type(result[key]) is not int for key in keys):
            raise ValueError("result-integer-cents")
        if tuple(result[key] for key in keys) != expected:
            raise ValueError("quote-value")
        if items != before:
            raise ValueError("input-mutation")
        checks += 1
    invalid = [None, {}, [], "items", [None], [{}],
               ({"unit_price_cents": 100, "quantity": 1},),
               [{"quantity": 1}],
               [{"unit_price_cents": 100}],
               [{"unit_price_cents": 100, "quantity": 1, "extra": 1}],
               [{"unit_price_cents": 100, "quantity": 1},
                {"unit_price_cents": 100, "quantity": 0}]]
    for field, values in (("unit_price_cents", [-1, True, 1.5, "100", None]),
                          ("quantity", [0, -1, True, 1.5, "2", None])):
        for value in values:
            item = {"unit_price_cents": 100, "quantity": 1}
            item[field] = value
            invalid.append([item])
    for items in invalid:
        before = copy.deepcopy(items)
        try:
            quote(items)
        except ValueError:
            pass
        else:
            raise ValueError("invalid-input-accepted")
        if items != before:
            raise ValueError("invalid-input-mutation")
        checks += 1
    return checks


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("candidate", type=pathlib.Path)
    args = parser.parse_args(argv)
    # Verifier 提供的 worktree 边界另行约束；这里不沿 candidate symlink 导入。
    if args.candidate.is_symlink() or not args.candidate.is_file():
        print("order-quote: candidate-not-regular", file=sys.stderr)
        return 1
    try:
        spec = importlib.util.spec_from_file_location("order_quote_candidate", args.candidate)
        if spec is None or spec.loader is None:
            raise ValueError("candidate-loader")
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        count = check(module.quote_order)
    except BaseException as error:
        # 不回显候选异常文本：可能包含源码、路径或凭据。
        print("order-quote: FAIL (" + type(error).__name__ + ")", file=sys.stderr)
        return 1
    print("order-quote: PASS checks=" + str(count))
    return 0


if __name__ == "__main__":
    sys.exit(main())
