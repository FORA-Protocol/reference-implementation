// The add-a-domain wizard.
//
// It holds the publisher's answers in the browser and saves nothing until the
// last button. Saving holds the hostname for this publisher, so it waits until
// they have been through every step; whether the saved domain is verified is
// decided on the server, by the checks it runs then.
//
// Almost nothing is decided here. Every "may I go on" is a passed flag the
// server sent: the rules for what a valid discovery document is, what a
// deployed package answers and what a price reads as all live where they are
// enforced, and a copy of any of them in this file would be a second set to
// keep in step. What this file does is remember the answers, ask, and draw.

function wizard() {
  // The page carries its starting state in an attribute, which the browser
  // un-escapes on the way out of dataset. Reading it here rather than fetching
  // it keeps the wizard from rendering empty while a first call runs.
  const boot = JSON.parse(document.getElementById('wizard').dataset.boot);
  const STORE = 'fora.add-domain';
  // What this file expects a stored record to mean. Raise it whenever the shape
  // changes or whenever a release fixes how a record is written: a record from
  // before such a fix is still readable, which is the dangerous kind of stale.
  // One written by a version that saved a domain and the network found for a
  // DIFFERENT domain restored exactly that pair, and the page reported a
  // network for an address nobody had looked up.
  const VERSION = 6;
  // Set when the page reopens a domain already saved, to finish its setup.
  // Its answers are the ones saved on the Exchange, so nothing about it is
  // read from or written to this tab's storage.
  const resume = boot.resume || null;
  const remembered = rememberedRecord(STORE, VERSION, resume);
  const opening = openingAnswers(resume, remembered);

  const blankDocument = () => ({ passed: false, reasons: [] });
  const blankEdge = () => ({ passed: false, state: '', message: '', checks: [] });

  return {
    // The calls to the console, one per step or check, are wizard-calls.js;
    // moving between steps is wizard-flow.js. Both are methods only: a getter
    // spread into this object would be copied as the value it had then.
    ...globalThis.wizardCalls(),
    ...globalThis.wizardFlow(),
    steps: boot.steps,
    paths: boot.paths,
    hasDomains: boot.has_domains === true,
    resuming: resume !== null,
    functions: boot.functions,
    currency: boot.currency,
    unit: boot.unit,

    // Restored by the step's name, not by its position. The business-name step
    // disappears once it is answered, so a position saved while it was there
    // points one step too far along when the page is opened again -- which
    // dropped the publisher on "publish this file" for a domain they had not
    // typed yet.
    index: 0,
    name: remembered.name || boot.display_name || '',
    domain: opening.domain || '',
    paths_: opening.paths || '',
    network: opening.network || '',
    taken: false,
    unsupported: false,
    limitReached: false,
    // yours is set when the domain typed is already one of this publisher's;
    // the step then links to it instead of adding it again.
    yours: false,
    yoursVerified: false,
    yoursLink: '',
    message: '',
    // Not remembered, on purpose. The files, the deployment steps, the page
    // the offer previews and what the site's own licensing said are all
    // DERIVED from the domain, the CDN and the paths -- cheap to ask for again
    // and wrong the moment any of those changes. A stored copy is a file the
    // publisher is told to publish that is no longer the file we would accept.
    files: [],
    edgeSteps: [],
    page: null,
    termsSource: '',
    terms: remembered.terms || boot.terms,
    // termsSettled is set once the licensing step has opened on this domain.
    // From then on the terms on screen are the publisher's, and reading the
    // site's own licensing document again after a reload does not overwrite
    // them.
    termsSettled: opening.terms_settled === true,
    // importReplacesManualTerms is the last step's checkbox: whether the
    // automatic import may replace the terms of the site's pages that are
    // already in the catalog. Its own answer, beside the terms rather than
    // inside them: the terms are what the offer preview is drawn from and what
    // the terms check reads, and this changes neither.
    importReplacesManualTerms: opening.import_replaces_manual_terms === true,
    // lookedUp is the domain the network answer on screen is for. The lookup
    // writes back the spelling the server normalized, and that write is not
    // the publisher changing the address.
    lookedUp: opening.network ? opening.domain || '' : '',
    termsNote: '',
    offer: [],
    offerWarning: '',
    checks: { document: blankDocument() },
    edge: blankEdge(),
    // skipped is the steps passed over with Skip for now, by key. The progress
    // header marks them as skipped rather than done, until the publisher comes
    // back and leaves the step with Next.
    skipped: opening.skipped,

    busy: false,
    error: '',
    // When the step's check last came back. A second press that finds the
    // same thing redraws the same warning, which reads as a button that does
    // nothing; the time is what shows it asked again.
    checkedAt: '',

    init() {
      const at = this.steps.findIndex((s) => s.key === remembered.step);
      if (at >= 0) this.index = at;
      // The paths field is spelled paths_ because paths is the route table.
      // Alpine runs a watcher after the change that caused it, so the lookup's
      // own write-back of the normalized name reaches this too. Comparing with
      // the name the answer is for is what tells the two apart: without it,
      // "Example.COM" was looked up, answered for "example.com", and the
      // answer was cleared again before the publisher could press Next.
      this.$watch('domain', (value) => {
        if (value === this.lookedUp) return;
        this.invalidate();
        // A different domain has its own licensing document to read, and its
        // own catalog to answer for.
        this.termsSettled = false;
        this.importReplacesManualTerms = false;
      });
      this.$watch('paths_', () => this.invalidate());
      this.$watch('terms', () => this.previewOffer(), { deep: true });
      // Choosing to charge fills the price in. Left empty it answers the first
      // click with "these terms cannot be offered", which is a refusal of
      // something the publisher has not said yet. Zero is not an option: a
      // per-access term priced at zero IS a free term, and the builder says so
      // rather than quietly selling nothing.
      this.$watch('terms.pricing_model', (model) => {
        if (model === 'per_unit' && !this.terms.rate.trim()) {
          this.terms.rate = boot.default_rate;
        }
      });
      // A saved domain opens where its setup stands, which only the site can
      // say: the server asks it and answers with the step.
      if (this.resuming) {
        void this.resumeAt();
        return;
      }
      this.enter(this.index);
    },

    get step() {
      const s = this.steps[this.index];
      return s ? s.key : '';
    },
    get last() {
      return this.index === this.steps.length - 1;
    },
    // The CDN's own name, as the server spelled it. Mapping the token here as
    // well would be a second copy of the same table, so a reload that restores
    // the token without the label re-asks rather than guesses.
    networkLabel: opening.network_label || '',
    // packageStep is where the publisher installs the package for whatever the
    // check found. It is a position in the list the server sent rather than a
    // constant, for the reason the progress header's numbers are: a publisher
    // who already has a business name is not shown that step, and everything
    // after it moves up one.
    get packageStep() {
      return this.steps.findIndex((s) => s.key === 'package') + 1;
    },
    // networkNotice says what the check found and what follows from it. The
    // network is not a fact about the domain the publisher has to do anything
    // with on its own -- it decides which package they install and where, so
    // the answer says that rather than leaving them to connect the two.
    get networkNotice() {
      return `${this.networkLabel} serves this domain, and we have a package for it. You install that package at step ${this.packageStep}.`;
    },
    // supportEmail is who answers when the CDN is one a publisher cannot set
    // up here. The dead end is the only screen with no way forward, so the
    // address on it is the way forward.
    supportEmail: boot.support_email || '',
    // edgeFailures is what to show when the package step does not pass: the
    // questions that did not come back clean, and nothing else.
    get edgeFailures() {
      return this.edge.checks.filter((c) => c.outcome !== 'passed');
    },
    get manifestFiles() {
      return this.files.filter((f) => f.name === 'fora.json');
    },
    get edgeFiles() {
      return this.files.filter((f) => f.name !== 'fora.json');
    },
    // needsCheck decides whether the step offers a Check button instead of
    // relying on the one the wizard ran when the step opened.
    get needsCheck() {
      if (this.step === 'document') return !this.checks.document.passed;
      if (this.step === 'package') return !this.edge.passed;
      return false;
    },
    get skippable() {
      return skippableOn(this);
    },
    get canAdvance() {
      switch (this.step) {
        case 'name':
          return this.name.trim() !== '';
        // The lookup is what opens this step, not the typing: a network we
        // cannot equip is a dead end, and there is no point walking someone
        // through publishing a file for it.
        case 'domain':
          return this.network !== '' && !this.taken && !this.unsupported;
        case 'document':
          return this.checks.document.passed;
        case 'package':
          return this.edge.passed;
        default:
          return true;
      }
    },

    // save records the wizard where it stands. It is called on a step change
    // and nowhere else.
    //
    // Saving on every edit put inconsistent pairs in storage: a domain typed
    // but never checked, sitting beside the network found for the domain
    // before it. Restoring that showed a green "Cloudflare serves this domain"
    // for an address nobody had looked up, and let the publisher walk on. A
    // step's answers and what was found about them are one thing, so they are
    // written together, when the publisher leaves the step.
    save() {
      if (this.resuming) return;
      try {
        sessionStorage.setItem(
          STORE,
          JSON.stringify({
            v: VERSION,
            // What the publisher answered, and the one finding everything else
            // is built from. Nothing derived: see the fields it is not beside.
            step: this.step,
            name: this.name,
            domain: this.domain,
            paths: this.paths_,
            network: this.network,
            terms: this.terms,
            terms_settled: this.termsSettled,
            import_replaces_manual_terms: this.importReplacesManualTerms,
            skipped: this.skipped,
          }),
        );
      } catch {
        /* a session that cannot remember starts again */
      }
    },

    // forget drops this tab's record of a domain being added. A wizard
    // reopening a saved domain wrote none, and the record this tab holds is
    // then about another domain the publisher may still be adding.
    forget() {
      if (this.resuming) return;
      try {
        sessionStorage.removeItem(STORE);
      } catch {
        /* nothing to do */
      }
    },

    // invalidate drops everything that was true of a different domain. Without
    // it, changing the address on the second step would leave two green ticks
    // standing for checks made against the old one.
    invalidate() {
      this.lookedUp = '';
      this.clearLookup();
      this.files = [];
      this.edgeSteps = [];
      this.page = null;
      this.termsSource = '';
      this.edge = blankEdge();
    },

    // clearLookup drops everything a lookup found: the network and its name,
    // the dead end, the domain being somebody else's or already this
    // publisher's, the limit, the sentence that came with any of them, and what
    // the Exchange said about the published document.
    clearLookup() {
      this.network = '';
      this.networkLabel = '';
      this.taken = false;
      this.unsupported = false;
      this.limitReached = false;
      this.yours = false;
      this.yoursVerified = false;
      this.yoursLink = '';
      this.message = '';
      this.checks.document = blankDocument();
    },
  };
}

// rememberedRecord is what an earlier visit in this tab left behind, when it
// was written by this version of the wizard. A wizard reopening a saved domain
// reads nothing from the tab: the domain's answers are the saved ones.
//
// Browser storage throws in a private window and can come back empty after
// site data is cleared, so every use of it is guarded and the wizard works
// without it -- one that loses its memory simply starts again.
function rememberedRecord(store, version, resume) {
  if (resume) return {};
  try {
    const held = JSON.parse(sessionStorage.getItem(store));
    return held && held.v === version ? held : {};
  } catch {
    return {};
  }
}

// openingAnswers is what the wizard opens with: a saved domain's answers when
// it reopens one, and otherwise whatever this tab remembers. Terms saved with
// the domain are already the publisher's, so its licensing step opens on them
// as settled. A domain saved before its terms were chosen opens that step the
// way a first visit does, filled in from the site's own licensing document.
// Nothing is marked skipped on a reopened domain: every step before the one it
// reopens on is one its site passes now.
function openingAnswers(resume, remembered) {
  if (!resume) return { skipped: [], ...remembered };
  return {
    skipped: [],
    domain: resume.domain,
    paths: resume.content_paths,
    network: resume.cdn,
    network_label: resume.network_label,
    terms_settled: resume.terms_set === true,
    import_replaces_manual_terms: resume.import_replaces_manual_terms,
  };
}

// skippableOn decides whether Skip for now is offered on the wizard's current
// step: on every step from the domain on, but not on a dead end, which has its
// own way back. On the file and package steps it is offered while their check
// has not passed, like Check again; a step that passed is left with Next.
function skippableOn(w) {
  switch (w.step) {
    case 'name':
      return false;
    case 'domain':
      return !w.unsupported;
    case 'document':
    case 'package':
      return w.needsCheck;
    default:
      return true;
  }
}

// Alpine reads the component by name from the page's x-data, so it is a global.
globalThis.wizard = wizard;
