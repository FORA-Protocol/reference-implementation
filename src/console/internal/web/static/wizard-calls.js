// The add-a-domain wizard's calls to the console: one method per step or
// read, each asking the server and writing what it answered into the wizard,
// and the two every call goes through -- post sends one, run shows the spinner
// and puts a failure on the page. wizard.js spreads them into its component,
// so `this` is the wizard; this file holds no state of its own. The page loads
// it before wizard.js.

function wizardCalls() {
  return {
    // post sends one call. A refusal carries the status it was answered with,
    // so a caller can tell one that asking again would change from one it
    // would not.
    async post(path, body) {
      const headers = { 'Content-Type': 'application/json' };
      const res = await fetch(path, { method: 'POST', headers, body: JSON.stringify(body) });
      const parsed = await res.json().catch(() => null);
      if (!res.ok) {
        const err = new Error(parsed?.error || 'Something went wrong. Please try again.');
        err.status = res.status;
        throw err;
      }
      return parsed;
    },

    // run wraps one call the publisher made: it shows the spinner, and it puts
    // a failure on the page rather than in the console, where the publisher
    // would never see it.
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

    // submitName is the business step's Next: it registers the business under
    // the name typed, without the spaces around it, and moves on. The name
    // cannot be changed afterwards, so the wizard does not go back to the step.
    async submitName() {
      if (!this.canAdvance) return undefined;
      const out = await this.run(() =>
        this.post(this.paths.publisher, { display_name: this.name.trim() }),
      );
      if (!out) return undefined;
      return this.advance();
    },

    // checkNetwork asks the one question the publisher cannot answer: which
    // network serves their domain, and whether they could save it. Enter runs
    // it, and so does Next when it has not run for the address on screen.
    //
    // A saved domain is not looked up: what serves it is what it was saved
    // with, and looking it up would only answer that it is already theirs.
    async checkNetwork() {
      const asked = this.domain;
      if (this.saved || !asked.trim()) return null;
      const out = await this.run(() => this.post(this.paths.lookup, { domain: asked.trim() }));
      // An answer for an address the publisher has since changed is an answer
      // about a different domain.
      if (!out || this.domain !== asked) return null;
      this.lookedUp = out.domain;
      this.domain = out.domain;
      this.takeLookup(out);
      return out;
    },

    // takeLookup draws what the lookup answered. Each answer starts from
    // nothing found, so what an earlier answer for the same domain found does
    // not stay on screen beside it. Each of its early answers ends the step on
    // its own, and Next stays shut for all of them.
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
      // draw and no dead end: the answer is the sentence.
      this.limitReached = out.limit_reached === true;
      if (this.limitReached) {
        this.message = out.message || '';
        return;
      }
      this.network = out.provider;
      this.networkLabel = out.provider_label;
      this.taken = out.taken === true;
      this.unsupported = !out.supported;
      this.message = out.message || '';
    },

    // saveDomain is the domain step's Next. It looks the domain up when that
    // has not happened for the address on screen, and saves it at once when
    // the answer is a network the publisher can set up here and the domain is
    // free to save. From then on the domain is held for this publisher and
    // listed as not verified, so leaving loses nothing; the address moves to
    // the page that finishes its setup, so a reload reopens it there too.
    async saveDomain() {
      if (this.saved) return this.advance();
      if (this.lookedUp !== this.domain && !(await this.checkNetwork())) return undefined;
      if (!this.network || this.taken || this.unsupported || this.limitReached || this.yours) {
        return undefined;
      }
      const out = await this.run(() =>
        this.post(this.paths.register, {
          domain: this.domain.trim(),
          cdn: this.network,
          content_paths: this.paths_,
        }),
      );
      if (!out) return undefined;
      // What was saved, which for a domain saved before is what it was saved
      // with rather than what was typed this time.
      this.saved = true;
      this.lookedUp = out.domain;
      this.domain = out.domain;
      this.network = out.cdn;
      this.paths_ = out.content_paths;
      history.replaceState(null, '', out.setup);
      return this.advance();
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
        // opens. After that the terms are the publisher's own, which this
        // must not replace.
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
    // busy flag locked the buttons while nothing was being saved.
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

    // saveTerms is the licensing step's Next: it saves the terms on screen on
    // the saved domain, with the sitemap the step found and the answer to the
    // catalog question beside them. A verified domain is sold under the terms
    // it was verified with, so they are shown, not saved again.
    async saveTerms() {
      if (this.verified) return this.advance();
      const out = await this.run(() =>
        this.post(this.paths.terms, {
          domain: this.domain.trim(),
          terms: this.terms,
          sitemap_url: this.page?.sitemap || '',
          import_replaces_manual_terms: this.importReplacesManualTerms,
        }),
      );
      if (!out) return undefined;
      // A domain another tab verified meanwhile kept its terms; the last step
      // says so.
      if (out.verified) this.connection.verified = true;
      return this.advance();
    },

    // prepareFiles asks for the saved domain's files, built from what the
    // Exchange holds for it, and the deployment steps on its network.
    async prepareFiles() {
      if (this.files.length) return;
      const out = await this.run(() => this.post(this.paths.files, { domain: this.domain.trim() }));
      if (!out) return;
      this.files = out.files;
      this.edgeSteps = out.edge_steps || [];
      this.deployed = out.deployed || '';
    },
  };
}

globalThis.wizardCalls = wizardCalls;
