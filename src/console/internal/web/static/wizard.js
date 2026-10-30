// The add-a-domain wizard.
//
// Each step's Next saves what the step asked, on the Exchange: the business
// name, on the step an account that has not named its business opens on, then
// the domain, then its terms. The last step asks the publisher's own site where
// their discovery file and their package stand, again every few seconds while
// it is shown, and the server verifies the domain once both pass. Nothing is
// kept in the browser. The Exchange holds what was saved, and a domain the
// publisher comes back to reopens from there.
//
// Almost nothing is decided here. Every "may I go on" is a passed flag the
// server sent: the rules for what a valid discovery document is, what a
// deployed package answers and what a price reads as all live where they are
// enforced, and a copy of any of them in this file would be a second set to
// keep in step. What this file does is hold the answers on screen, ask, and
// draw.

function wizard() {
  // The page carries its starting state in an attribute, which the browser
  // un-escapes on the way out of dataset. Reading it here rather than fetching
  // it keeps the wizard from rendering empty while a first call runs.
  const boot = JSON.parse(document.getElementById('wizard').dataset.boot);
  // Set when the page reopens a domain already saved, to finish its setup.
  // Its answers, and the step it opens on, are the server's.
  const resume = boot.resume || null;
  const opening = openingAnswers(resume);

  return {
    // The calls to the console are wizard-calls.js, moving between steps is
    // wizard-flow.js and the last step's checks are wizard-connect.js. All
    // three are methods only: a getter spread into this object would be
    // copied as the value it had then.
    ...globalThis.wizardCalls(),
    ...globalThis.wizardFlow(),
    ...globalThis.wizardConnect(),
    steps: boot.steps,
    paths: boot.paths,
    functions: boot.functions,
    currency: boot.currency,
    unit: boot.unit,
    supportEmail: boot.support_email || '',

    // The business name, on the step only an account that has not named its
    // business is shown. It opens empty, for the publisher to type.
    name: '',
    index: 0,
    domain: opening.domain,
    paths_: opening.paths,
    network: opening.network,
    // The CDN's own name, as the server spelled it. Mapping the token here as
    // well would be a second copy of the same table.
    networkLabel: opening.networkLabel,
    // saved is set once the domain is saved on the Exchange: reopened to
    // finish its setup, or saved by this visit's domain step. From then on the
    // domain and its paths are what was saved, and the domain step only moves
    // on.
    saved: resume !== null,
    taken: false,
    unsupported: false,
    limitReached: false,
    // yours is set when the domain typed is already one of this publisher's;
    // the step then links to it instead of adding it again.
    yours: false,
    yoursVerified: false,
    yoursLink: '',
    message: '',
    // lookedUp is the domain the lookup answer on screen is for. The lookup
    // writes back the spelling the server normalized, and that write is not
    // the publisher changing the address.
    lookedUp: '',
    // The saved domain's files, the deployment steps on its network and where
    // its package runs once it answers. Asked for when the last step opens.
    files: [],
    edgeSteps: [],
    deployed: '',
    page: null,
    termsSource: '',
    terms: boot.terms,
    // termsSettled is set once the licensing step has opened on this domain.
    // From then on the terms on screen are the publisher's, and reading the
    // site's own licensing document again does not overwrite them. Terms saved
    // with the domain are the publisher's already.
    termsSettled: opening.termsSettled,
    // importReplacesManualTerms is the licensing step's checkbox: whether the
    // automatic import may replace the terms of the site's pages that are
    // already in the catalog. Its own answer, beside the terms rather than
    // inside them: the terms are what the offer preview is drawn from, and
    // this changes nothing in it.
    importReplacesManualTerms: opening.importReplacesManualTerms,
    termsNote: '',
    offer: [],
    offerWarning: '',
    // connection is what the last check of the last step answered.
    connection: blankConnection(),
    busy: false,
    error: '',
    // checkError is the last step's own failure, kept apart from error, which
    // the calls the publisher makes write: a check that repeats on its own must
    // never clear what one of those calls put on the page.
    checkError: '',
    // When the last step's check last came back. A check that finds the same
    // thing redraws the same status, which reads as a page that is not
    // looking; the time is what shows it asked again.
    checkedAt: '',
    // checking is set while one check of the last step is in flight, so the
    // repeating check and a press of Check now never overlap.
    checking: false,
    // timer is the last step's repeating check, while the step is shown.
    timer: null,
    // visibility is the page's hidden-or-shown listener, kept so destroy can
    // take it away again.
    visibility: null,

    init() {
      // The paths field is spelled paths_ because paths is the route table.
      // Alpine runs a watcher after the change that caused it, so the lookup's
      // own write-back of the normalized name reaches this too. Comparing with
      // the name the answer is for is what tells the two apart.
      this.$watch('domain', (value) => {
        if (this.saved || value === this.lookedUp) return;
        this.lookedUp = '';
        this.clearLookup();
        // A different domain has its own licensing document to read.
        this.termsSettled = false;
        this.page = null;
      });
      this.$watch('terms', () => this.previewOffer(), { deep: true });
      // Choosing to charge fills the price in. Left empty it answers the first
      // click with "these terms cannot be offered", which is a refusal of
      // something the publisher has not said yet.
      this.$watch('terms.pricing_model', (model) => {
        if (model === 'per_unit' && !this.terms.rate.trim()) {
          this.terms.rate = boot.default_rate;
        }
      });
      // The last step checks the site only while it can be seen: a tab left
      // in the background stops asking, and asks at once on its return.
      this.visibility = () => this.onVisibility();
      document.addEventListener('visibilitychange', this.visibility);
      const at = resume ? this.steps.findIndex((s) => s.key === resume.step) : 0;
      this.enter(Math.max(at, 0));
    },

    // destroy is Alpine's call when the component is removed: the checks stop,
    // and the page no longer tells it when it is hidden.
    destroy() {
      this.stopChecking();
      document.removeEventListener('visibilitychange', this.visibility);
    },

    get step() {
      const s = this.steps[this.index];
      return s ? s.key : '';
    },
    get verified() {
      return this.connection.verified === true;
    },
    // pageError is what the page's one alert shows: the failure of a call the
    // publisher made, or else the last step's check's own.
    get pageError() {
      return shownError(this);
    },
    // connectStep is where the publisher installs the package. It is a
    // position in the list the server sent rather than a constant, so this
    // sentence and the progress header count the same list.
    get connectStep() {
      return this.steps.findIndex((s) => s.key === 'connect') + 1;
    },
    // networkNotice says what the lookup found and what follows from it: which
    // package the publisher installs, and where.
    get networkNotice() {
      return `${this.networkLabel} serves this domain, and we have a package for it. You install that package at step ${this.connectStep}.`;
    },
    get canAdvance() {
      switch (this.step) {
        case 'name':
          return nameReady(this);
        // Next runs the lookup when none ran for this address, so it waits
        // only on an answer that ends the step.
        case 'domain':
          return (
            this.saved ||
            (this.domain.trim() !== '' &&
              !this.taken &&
              !this.unsupported &&
              !this.limitReached &&
              !this.yours)
          );
        case 'terms':
          return true;
        default:
          return false;
      }
    },
    // deadEnd is the domain step's answer that its network is not one the
    // publisher can set up here: the step then has no Next, only the link back
    // to their domains.
    get deadEnd() {
      return atDeadEnd(this);
    },
    // canFinishLater is whether the step offers Finish later. The business
    // step does not: it is left only through its Next, once the business is
    // named. The dead end has its own way back, and a verified domain has
    // nothing left to finish.
    get canFinishLater() {
      return laterOffered(this);
    },
    get manifestFile() {
      return this.files.find((f) => f.name === 'fora.json') || null;
    },
    get packageFiles() {
      return this.files.filter((f) => f.name !== 'fora.json');
    },
    // The badges say each task's state in two or three words; the sentence
    // under the task says the rest.
    get fileDone() {
      return this.verified || this.connection.file.passed;
    },
    get packageDone() {
      return this.verified || this.connection.package.passed;
    },
    get fileBadge() {
      if (this.fileDone) return 'Served';
      const state = this.connection.file.state;
      return state === '' || state === 'missing' ? 'Not found yet' : 'Needs a fix';
    },
    get packageBadge() {
      if (this.packageDone) return 'Answering';
      const state = this.connection.package.state;
      return state === '' || state === 'absent' ? 'Not installed yet' : 'Needs a fix';
    },
    // packageProblems is what to list under a package task that has not
    // passed: the questions that did not come back clean, and nothing else.
    get packageProblems() {
      return this.connection.package.checks.filter((c) => c.outcome !== 'passed');
    },

    // clearLookup drops everything a lookup found: the network and its name,
    // the dead end, the domain being somebody else's or already this
    // publisher's, the limit, and the sentence that came with any of them.
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
    },
  };
}

// openingAnswers is what the wizard opens with: a saved domain's answers when
// it reopens one, and nothing typed yet otherwise.
function openingAnswers(resume) {
  if (!resume) {
    return {
      domain: '',
      paths: '',
      network: '',
      networkLabel: '',
      termsSettled: false,
      importReplacesManualTerms: false,
    };
  }
  return {
    domain: resume.domain,
    paths: resume.content_paths,
    network: resume.cdn,
    networkLabel: resume.network_label,
    termsSettled: resume.terms_set === true,
    importReplacesManualTerms: resume.import_replaces_manual_terms === true,
  };
}

// atDeadEnd reports whether wizard w stands on the domain step with a network
// the publisher cannot set up here.
function atDeadEnd(w) {
  return w.step === 'domain' && w.unsupported;
}

// laterOffered reports whether the step wizard w stands on offers Finish later:
// any step but the business step, away from the dead end, until the domain is
// verified.
function laterOffered(w) {
  return w.step !== 'name' && !w.deadEnd && !w.verified;
}

// nameReady reports whether the business step of wizard w can move on: with a
// name typed.
function nameReady(w) {
  return w.name.trim() !== '';
}

// shownError is the failure the page shows for wizard w: one from a call the
// publisher made first, since that is what they just did, and the last step's
// check's own otherwise.
function shownError(w) {
  return w.error || w.checkError;
}

// blankConnection is the last step before its first check has answered.
function blankConnection() {
  return {
    verified: false,
    file: { passed: false, state: '', status: '', reasons: [] },
    package: { passed: false, state: '', status: '', checks: [] },
  };
}

// Alpine reads the component by name from the page's x-data, so it is a global.
globalThis.wizard = wizard;
