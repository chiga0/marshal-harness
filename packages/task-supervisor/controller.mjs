// Compatibility export for v1–v6 callers. Runtime handle ownership lives in
// Execution; the v7 observer below accepts no execution/command capability.
export {TaskExecutionCoordinator as TaskSupervisor} from '../task-execution/controller.mjs';

export class TaskObserver {
  #observe;
  constructor({observe}) {
    if (typeof observe !== 'function') throw new TypeError('invalid observer');
    this.#observe = observe;
  }
  snapshot() {return structuredClone(this.#observe());}
}
