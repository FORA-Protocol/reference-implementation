// The add-a-domain wizard's last step, Connect your site: asking the
// publisher's site where its discovery file and its package stand -- once when
// the step opens, again every few seconds while it is shown, and at once when
// the publisher asks. The server verifies the domain on the check that finds
// both in place. wizard.js spreads these into its component, so `this` is the
// wizard; this file holds no state of its own. The page loads it before
// wizard.js.

function wizardConnect() {
  // How long the step waits between two checks. Deploying a package and
  // publishing a file take minutes, and a CDN takes seconds to minutes to
  // carry a change, so a check more often would only repeat the last answer.
  const RECHECK_MS = 15000;
  // The refusals no later check would change: the session ended, the address
  // is not confirmed, the domain is not this person's. Asking again every few
  // seconds would only repeat them.
  const FINAL = [401, 403, 404];

  return {
    // check asks the server once: the repeating check, and Check now. It stays
    // off run(), which is for the calls the publisher makes: run() holds the
    // page busy, which shuts Next and Check now, and clears the page's error
    // when it starts. A check that repeats on its own must do neither, so it
    // keeps its own in-flight flag and its own error. An answer that comes back
    // after the publisher has left the step is for a step no longer shown, and
    // is dropped.
    async check() {
      if (this.step !== 'connect' || this.checking) return;
      this.checking = true;
      try {
        this.takeCheck(await this.post(this.paths.check, { domain: this.domain.trim() }));
      } catch (err) {
        this.takeCheckFailure(err);
      } finally {
        this.checking = false;
      }
    },

    // takeCheck draws what a check answered, unless the publisher has left the
    // step since it was asked, and stops the checks once the domain is verified.
    takeCheck(out) {
      if (this.step !== 'connect') return;
      this.connection = out;
      this.checkError = '';
      this.checkedAt = new Date().toLocaleTimeString();
      if (out.verified) this.stopChecking();
    },

    // takeCheckFailure puts a check's failure on the step, unless the publisher
    // has left it since the check was asked, and stops the checks on a refusal
    // no later check would change.
    takeCheckFailure(err) {
      if (this.step !== 'connect') return;
      this.checkError = err.message;
      if (FINAL.includes(err.status)) this.stopChecking();
    },

    // checkNow is the Check now button: a check at once, and the saved
    // domain's files again when the step could not get them when it opened.
    // The files are asked for again only when the publisher presses it, never
    // on the timer, so a build that keeps failing is not rebuilt every few
    // seconds.
    async checkNow() {
      if (!this.files.length) {
        await Promise.all([this.prepareFiles(), this.check()]);
        return;
      }
      await this.check();
    },

    // startChecking checks once, and then again every RECHECK_MS while the
    // page can be seen and the domain is not verified.
    startChecking() {
      this.stopChecking();
      if (this.verified) return;
      void this.check();
      if (document.hidden) return;
      this.timer = setInterval(() => void this.check(), RECHECK_MS);
    },

    stopChecking() {
      if (this.timer === null) return;
      clearInterval(this.timer);
      this.timer = null;
    },

    // onVisibility stops the checks while the tab is in the background, and
    // checks at once when the publisher comes back to it -- typically from the
    // terminal they deployed the package in.
    onVisibility() {
      if (this.step !== 'connect' || this.verified) return;
      if (document.hidden) {
        this.stopChecking();
        return;
      }
      this.startChecking();
    },
  };
}

// wizard.js reads the methods by name when it builds its component, so this
// is a global, the way wizardCalls is.
globalThis.wizardConnect = wizardConnect;
