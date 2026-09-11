// One shutdown obligation for signals and terminal service failure. Startup must
// publish every acquired resource before cleanup can finish; no owner recovery.
export function cliShutdown(resources, completed) {
  let release, pending, failure = null;
  const startup = new Promise(resolve => {release = resolve;});
  return {
    get requested() {return !!pending;},
    started() {release();},
    stop(code = null) {
      failure ??= code;
      if (!pending) pending = (async () => {
        await startup;
        const {edge, service} = resources();
        let cleanupFailed = false, result;
        try {await edge?.close();} catch {cleanupFailed = true;}
        try {if (service) result = await service.shutdown();} catch {cleanupFailed = true;}
        const final = result ? {state: result.state, clean: !cleanupFailed && result.shutdownClean === true,
          code: result.failure ?? failure ?? (cleanupFailed ? 'service_shutdown_unavailable' : null)} :
          service ? {state: 'failed', clean: false, code: failure ?? 'service_shutdown_unavailable'} : null;
        completed(final, failure || cleanupFailed || final?.code || final && !final.clean ? 1 : 0);
        return final;
      })();
      return pending;
    },
  };
}
