// The add-a-domain wizard's movement between steps: how the progress header
// draws each step, what Next does on each one, going back, and entering a
// step. wizard.js spreads these into its component, so `this` is the wizard;
// this file holds no state of its own. The page loads it before wizard.js.

function wizardFlow() {
  return {
    stepClass(i) {
      if (i < this.index) return this.isSkipped(i) ? 'wiz-step wiz-skipped' : 'wiz-step wiz-done';
      if (i === this.index) return 'wiz-step wiz-now';
      return 'wiz-step';
    },

    async next() {
      switch (this.step) {
        case 'name':
          return this.submitName();
        case 'terms':
          return this.finish();
        default:
          return this.advance();
      }
    },

    // isSkipped reports whether the step at i was passed over with Skip for
    // now.
    isSkipped(i) {
      return this.skipped.includes(this.steps[i].key);
    },

    back() {
      if (this.index === 0) return;
      this.goBack(this.index - 1);
    },

    goBack(i) {
      this.enter(i);
    },

    // advance is Next on a step that passed, so the step is done now, even if
    // it was skipped before.
    advance() {
      this.skipped = this.skipped.filter((key) => key !== this.step);
      if (this.index < this.steps.length - 1) this.enter(this.index + 1);
    },

    // skip is Skip for now. On the domain step it leaves with nothing saved,
    // which is how a publisher ends their registration with no domain. On the
    // file and package steps it moves on to the next step, and the step it
    // leaves is marked skipped. On the licensing step it saves the domain with
    // no terms, listed as not verified; a reopened domain is saved already, so
    // there it only leaves.
    skip() {
      if (!this.skippable) return;
      switch (this.step) {
        case 'domain':
          return this.leave();
        case 'terms':
          return this.resuming ? this.leave() : this.saveDomain(null);
        default:
          this.skipped = [...this.skipped.filter((key) => key !== this.step), this.step];
          this.enter(this.index + 1);
      }
    },

    // leave goes back to the publisher's domains with nothing saved, and drops
    // what this tab remembers, so the next visit starts clean.
    leave() {
      this.forget();
      this.navigate(this.paths.home);
    },

    // enter moves to a step and runs that step's check once, so the screen
    // opens knowing where the publisher stands: someone who already serves
    // their file sees it pass without pressing anything.
    //
    // It does not move on by itself, even when the check passes. It used to,
    // which made a publisher whose file and package were both already in place
    // press Next on the domain step and land on the last one, never seeing the
    // two steps in between or learning why they were skipped. A step that
    // passes shows that it passed and unlocks Next; leaving it stays the
    // publisher's decision.
    //
    // check is false when the step's check has just been run for it, as when
    // the wizard reopens a saved domain where its setup stands.
    enter(i, { check = true } = {}) {
      this.index = Math.min(Math.max(i, 0), this.steps.length - 1);
      this.checkedAt = '';
      this.save();
      // Each check puts its own failure on the page, through run() or its own
      // catch, so none of them is awaited here: entering a step never waits.
      const step = this.step;
      if (step === 'domain' && this.network && !this.networkLabel) {
        // Restored from a previous visit: the token came back but the name for
        // it did not, and a screen that says " serves this domain" is worse
        // than one that asks again.
        void this.checkNetwork();
        return;
      }
      if (step === 'document' || step === 'package') {
        void this.prepareFiles().then(() => (check ? this.runCheck() : undefined));
        return;
      }
      if (step === 'terms') void this.prepareTerms();
    },

    // navigate leaves the wizard for another page. It is the one place the
    // wizard sets the browser's address, so a test can stand in for it and
    // read where the publisher was being sent.
    navigate(url) {
      window.location.href = url;
    },
  };
}

// wizard.js reads the methods by name when it builds its component, so this
// is a global, the way wizardCalls is.
globalThis.wizardFlow = wizardFlow;
