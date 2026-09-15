// No model, credentials or Worker process. Used only by the Node CLI test.
export default {
  providers: new Map([['fixture', {id: 'fixture', start() {throw new Error('fixture-start-unexpected');}}]]),
  prepare() {throw new Error('fixture-prepare-unexpected');},
  collect() {throw new Error('fixture-collect-unexpected');},
};
