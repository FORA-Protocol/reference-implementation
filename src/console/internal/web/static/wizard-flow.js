// The add-a-domain wizard's movement between steps: how the progress header
// draws each step, what Next does on each one, going back, and entering a
// step. wizard.js spreads these into its component, so `this` is the wizard;
// this file holds no state of its own. The page loads it before wizard.js.

function wizardFlow() {
  return {
    // stepDone reports whether the progress header marks step i done: a step
    // already passed, and the step on screen once the domain is verified.
    stepDone(i) {
      return i < this.index || (i === this.index && this.verified);
    },

    stepClass(i) {
      if (this.stepDone(i)) return 'wiz-step wiz-done';
      if (i === this.index) return 'wiz-step wiz-now';
      return 'wiz-step';
    },

    // canReturnTo reports whether Back and the progress header reopen step i:
    // any step already passed but the business step. Its name cannot be
    // changed once its Next has saved it, so that step is not shown again.
    canReturnTo(i) {
      return i >= 0 && i < this.index && this.steps[i].key !== 'name';
    },

    // next is the step's Next. Each one saves what its step asked: the
    // business name, the domain, then its terms. The last step has no Next; it
    // checks instead.
    async next() {
      switch (this.step) {
        case 'name':
          return this.submitName();
        case 'domain':
          return this.saveDomain();
        case 'terms':
          return this.saveTerms();
        default:
          return undefined;
      }
    },

    back() {
      this.goBack(this.index - 1);
    },

    goBack(i) {
      if (this.canReturnTo(i)) this.enter(i);
    },

    // advance moves to the next step once the step's answers are saved.
    advance() {
      if (this.index < this.steps.length - 1) this.enter(this.index + 1);
    },

    // enter moves to a step and starts what the step needs: the licensing step
    // reads the publisher's site once, and the last step asks for the saved
    // domain's files and starts checking. Leaving the last step stops the
    // checks. A failure belongs to the step it happened on, so a step is
    // entered with none on the page.
    //
    // It does not move on by itself, even when the last step's checks pass:
    // the step shows that the domain is verified, and where to go from there.
    enter(i) {
      this.index = Math.min(Math.max(i, 0), this.steps.length - 1);
      this.stopChecking();
      this.error = '';
      this.checkError = '';
      // Each call puts its own failure on the page, through run() or its own
      // catch, so none of them is awaited here: entering a step never waits.
      if (this.step === 'terms') void this.prepareTerms();
      if (this.step === 'connect') {
        void this.prepareFiles();
        this.startChecking();
      }
    },
  };
}

// wizard.js reads the methods by name when it builds its component, so this
// is a global, the way wizardCalls is.
globalThis.wizardFlow = wizardFlow;
