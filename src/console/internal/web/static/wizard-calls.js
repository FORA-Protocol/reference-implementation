// The add-a-domain wizard's calls to the console: one method per step or
// check, each asking the server and writing what it answered into the wizard,
// and the two every call goes through -- post sends one, run shows the spinner
// and puts a failure on the page. wizard.js spreads them into its component,
// so `this` is the wizard; this file holds no state of its own. The page loads
// it before wizard.js.

function wizardCalls() {
  return {
    async post(path, body) {
      const headers = { 'Content-Type': 'application/json' };
      const res = await fetch(path, { method: 'POST', headers, body: JSON.stringify(body) });
      const parsed = await res.json().catch(() => null);
      if (!res.ok) {
        throw new Error(parsed?.error || 'Something went wrong. Please try again.');
      }
      return parsed;
    },

    // run wraps one call: it shows the spinner, and it puts a failure on the
    // page rather than in the console, where the publisher would never see it.
    async run(fn) {
      this.busy = true;
      this.error = '';
      try {
        return await fn();
      } catch (err) {
        this.error = err.message;
        return null;
      } finally {
        this.busy = false;
      }
    },

    async submitName() {
      if (!this.canAdvance) return;
      const out = await this.run(() =>
        this.post(this.paths.publisher, { display_name: this.name.trim() }),
      );
      if (out) this.advance();
    },

    // checkNetwork asks the one question the publisher cannot answer: which
    // network serves their domain. An answer we have no package for ends the
    // wizard, and takes its stored state with it, so a reload lands on the
    // dashboard rather than back on a screen with no way forward.
    //
    // It does not move on by itself. The answer is the whole of what this step
    // produces, and a publisher who has just been told what serves their
    // domain should get to read it before the screen changes.
    //
    // A reopened domain is not looked up: what serves it is what it was saved
    // with, and looking it up would only answer that it is already theirs.
    async checkNetwork() {
      const asked = this.domain;
      if (this.resuming || !asked.trim()) return;
      const out = await this.run(() => this.post(this.paths.lookup, { domain: asked.trim() }));
      // An answer for an address the publisher has since changed is an answer
      // about a different domain.
      if (!out || this.domain !== asked) return;
      this.lookedUp = out.domain;
      this.domain = out.domain;
      this.takeLookup(out);
    },

    // takeLookup draws what the lookup answered. Each answer starts from
    // nothing found, so what an earlier answer for the same domain found -- a
    // network, before the publisher reached the limit in another tab -- does
    // not stay on screen beside it. Each of its early answers ends the step on
    // its own, and Next stays shut for all of them. A Check that is refused or
    // fails returns before this and keeps the last answer.
    takeLookup(out) {
      this.clearLookup();
      // A domain this publisher already holds is not looked up any further:
      // the step links to it.
      this.yours = out.yours === true;
      if (this.yours) {
        this.yoursVerified = out.verified === true;
        this.yoursLink = out.link || '';
        return;
      }
      // At the limit nothing else was looked up, so there is no network to
      // draw and no dead end: the answer is the sentence, and Next stays shut.
      this.limitReached = out.limit_reached === true;
      if (this.limitReached) {
        this.message = out.message || '';
        return;
      }
      this.network = out.provider;
      this.networkLabel = out.provider_label;
      this.taken = out.taken;
      this.unsupported = !out.supported;
      this.message = out.message || '';
      if (this.unsupported) {
        this.forget();
        return;
      }
      if (this.taken) return;
      // What the Exchange already said about the published document, so the
      // next step opens knowing whether it is there.
      this.checks.document = { passed: out.manifest.passed, reasons: out.manifest.reasons };
    },

    // resumeAt asks the server where the saved domain's setup stands and opens
    // the wizard there, with what the checks on the way found already drawn,
    // so the step does not ask the site again. A domain verified meanwhile has
    // nothing to finish, so the wizard goes to it instead.
    async resumeAt() {
      const out = await this.run(() => this.post(this.paths.progress, { domain: this.domain }));
      if (!out) return;
      if (out.verified) {
        this.navigate(this.paths.domain_prefix + encodeURIComponent(this.domain));
        return;
      }
      this.checks.document = out.document;
      if (out.edge.state) this.edge = out.edge;
      this.enter(
        this.steps.findIndex((s) => s.key === out.step),
        { check: false },
      );
      this.checkedAt = new Date().toLocaleTimeString();
    },

    async prepareFiles() {
      if (this.files.length) return;
      const out = await this.run(() =>
        this.post(this.paths.files, {
          domain: this.domain.trim(),
          cdn: this.network,
          content_paths: this.paths_,
        }),
      );
      if (!out) return;
      this.files = out.files;
      this.edgeSteps = out.edge_steps || [];
    },

    async runCheck() {
      this.checkedAt = '';
      if (this.step === 'document') await this.checkDocument();
      else if (this.step === 'package') await this.checkEdge();
      else return;
      if (!this.error) this.checkedAt = new Date().toLocaleTimeString();
    },

    async checkDocument() {
      const out = await this.run(() =>
        this.post(this.paths.check_manifest, { domain: this.domain.trim() }),
      );
      if (!out) return;
      this.checks.document = out;
    },

    async checkEdge() {
      const out = await this.run(() =>
        this.post(this.paths.check_edge, {
          domain: this.domain.trim(),
          content_paths: this.paths_,
        }),
      );
      if (!out) return;
      this.edge = out;
    },

    // prepareTerms reads the publisher's own site once: a page to price the
    // preview against, the sitemap the catalogue import will read, and
    // whatever their site already says about its licensing.
    async prepareTerms() {
      if (!this.page) {
        const out = await this.run(() =>
          this.post(this.paths.page, { domain: this.domain.trim() }),
        );
        if (!out) return;
        this.page = { title: out.page_title, url: out.page_url, sitemap: out.sitemap_url };
        // The site's document fills the form in the first time the step
        // opens. After that the terms are the publisher's own edits, which a
        // reload restores and this must not replace.
        if (!this.termsSettled) {
          if (out.terms) this.terms = out.terms;
          this.termsSource = out.terms_note || '';
          this.termsSettled = true;
        }
      }
      // previewOffer puts its own failure on the page.
      void this.previewOffer();
    },

    // previewOffer redraws the offer as the terms change. It stays off run():
    // it fires on every tick of a checkbox, and routing it through the shared
    // busy flag put "Checking…" beside Finish setup and locked the buttons
    // while nothing was being checked.
    async previewOffer() {
      if (this.step !== 'terms' || !this.page) return;
      try {
        const out = await this.post(this.paths.terms_preview, {
          page_title: this.page.title,
          page_url: this.page.url,
          terms: this.terms,
        });
        this.offer = out.lines || [];
        this.offerWarning = out.warning || '';
      } catch (err) {
        this.error = err.message;
      }
    },

    // finish is Finish setup, the last step's button: it saves the domain with
    // the terms on screen.
    async finish() {
      return this.saveDomain(this.terms);
    },

    // saveDomain is the only call that writes anything about the domain:
    // Finish setup sends the terms on screen, and Skip for now on the last step
    // sends none. The console saves the domain, then verifies it if it has
    // terms and the site's file and package both answer now. Either way the
    // publisher moves on: to the domain when it is verified, and to their list
    // of domains, where it shows as not verified, when it is not.
    async saveDomain(terms) {
      const out = await this.run(() =>
        this.post(this.paths.register, {
          domain: this.domain.trim(),
          cdn: this.network,
          content_paths: this.paths_,
          sitemap_url: this.page?.sitemap || '',
          terms,
          import_replaces_manual_terms: this.importReplacesManualTerms,
        }),
      );
      if (!out) return;
      this.forget();
      this.navigate(
        out.verified ? this.paths.domain_prefix + encodeURIComponent(out.domain) : this.paths.home,
      );
    },
  };
}

globalThis.wizardCalls = wizardCalls;
