// Module factories shared by the model tests (jest.mock factories must not
// close over test variables, so they require this file instead).
module.exports = {
  resourceClass: () => ({ colorForState: () => 'text-warning' }),
  array:         () => ({ insertAt: (ary, idx, ...objs) => ary.splice(idx, 0, ...objs) }),
};
