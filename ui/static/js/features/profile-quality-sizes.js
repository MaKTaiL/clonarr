// Per-profile quality sizes: the profile editor's Quality size tab.
//
// Sonarr v5 moved quality sizes off the instance-wide list under Media
// Management and onto each quality profile. On those versions the instance tab
// has nothing to write, so the choice lives here instead: one source per
// profile, plus the user's own numbers for individual qualities.
//
// The tab is shown only when the rule's instance keeps sizes per profile,
// which the backend answers from the shape of the instance's definition list.
// Never gated on the app being Sonarr: that would put the tab on Sonarr 4
// where it cannot work, and would miss the day Radarr does the same thing.

const QS_FIELDS = ['min', 'preferred', 'max'];

export default {
  state: {
    // Loaded per rule from GET /api/auto-sync/rules/{id}/quality-sizes.
    pdQSPerProfile: false,
    pdQSRows: [],
    pdQSSets: [],
    pdQSDefaultSetType: '',
    pdQSLoading: false,
    pdQSLoadedRuleId: null,
    // Editor state, saved with the rule.
    pdQSSource: '',
    pdQSOverrides: {},
    // The default view lists the qualities this profile allows. Measured
    // reason: a guide profile like WEB-1080p allows 2 of 22, so listing
    // everything would show 22 rows of inputs to set 2.
    pdQSShowAll: false,
    pdQSSaveSetName: '',
    // Set on the first edit in this tab. Until then the saved rule is the
    // truth; afterwards the editor's own state is, so a reload must not
    // overwrite an unsaved change.
    _pdQSTouched: false,
  },

  methods: {
    // --- Loading ---

    async pdQSLoad(force = false) {
      const rule = this.pdQSRule();
      if (!rule) {
        this.pdQSPerProfile = false;
        this.pdQSRows = [];
        return;
      }
      if (!force && this.pdQSLoadedRuleId === rule.id) return;
      // The response is slow: the backend reads the definition list and the
      // profile from the Arr instance. Capture the rule this request is for
      // and drop the answer if the editor has moved on, or one profile's rows
      // land under another profile's name, and an unsaved source gets copied
      // across with them.
      const forRuleId = rule.id;
      this.pdQSLoading = true;
      try {
        const r = await fetch(`/api/auto-sync/rules/${rule.id}/quality-sizes`);
        if (this.pdQSRule()?.id !== forRuleId) return;
        if (!r.ok) {
          // A profile the instance no longer has, or an instance that is
          // down. Say nothing rather than show an empty tab that looks like
          // "this profile has no qualities".
          this.pdQSPerProfile = false;
          this.pdQSRows = [];
          return;
        }
        const d = await r.json();
        if (this.pdQSRule()?.id !== forRuleId) return;
        this.pdQSPerProfile = !!d.perProfileSizes;
        this.pdQSRows = d.rows || [];
        this.pdQSSets = d.sets || [];
        this.pdQSDefaultSetType = d.defaultSetType || '';
        this.pdQSLoadedRuleId = rule.id;
        // The rule on disk is the truth on open; the editor's own state takes
        // over from the first edit.
        if (this.pdQSSource === '' && !this._pdQSTouched) {
          this.pdQSSource = d.source || '';
          this.pdQSOverrides = { ...(d.overrides || {}) };
        }
      } catch (e) {
        console.error('pdQSLoad:', e);
      } finally {
        this.pdQSLoading = false;
      }
    },

    // The rule this editor is attached to. The tab needs a saved rule because
    // the sizes are read from the live profile on the instance, so there is
    // nothing to show for a profile that does not exist yet.
    pdQSRule() {
      const inst = this.profileDetail?.instance;
      const arrId = this.profileDetail?._editLockedArrProfileId;
      if (!inst || !arrId) return null;
      return this.findAutoSyncRule(inst.id, arrId);
    },

    pdQSShowTab() {
      return !!this.pdQSPerProfile;
    },

    // --- The source selector ---

    // The set the source currently names, or null for off. Read from the
    // sets the endpoint shipped, which carry their own rows, so everything
    // below reflects what the user just picked rather than what was saved.
    pdQSActiveSet() {
      const src = this.pdQSSource;
      if (!src || src === 'off') return null;
      return (this.pdQSSets || []).find(s => s.value === src) || null;
    },

    // This set's entry for one quality, or null when it does not name it.
    // A set covering part of the profile is the normal case: the guide's
    // series set covers 14 of a v5 profile's 22 qualities.
    pdQSEntryFor(quality) {
      const set = this.pdQSActiveSet();
      if (!set) return null;
      return (set.entries || []).find(e => e.quality === quality) || null;
    },

    pdQSSourceLabel() {
      const src = this.pdQSSource;
      if (!src || src === 'off') return 'Off';
      const set = (this.pdQSSets || []).find(s => s.value === src);
      if (set) return set.origin === 'user' ? set.name : this.pdQSSetTitle(set.name);
      // A set that no longer exists: a user set deleted elsewhere, or a guide
      // set the guide dropped. Nothing gets written, so say that rather than
      // show a dead id.
      return 'Not found, nothing is written';
    },

    // The guide names its sets by type. "series" and "movie" read better with
    // a capital, and the sqp ones are known by their own spelling.
    pdQSSetTitle(type) {
      if (type === 'sqp-streaming') return 'SQP Streaming';
      if (type === 'sqp-uhd') return 'SQP UHD';
      return (type || '').charAt(0).toUpperCase() + (type || '').slice(1);
    },

    pdQSSetSource(value) {
      this.pdQSSource = value;
      this._pdQSTouched = true;
    },

    // The app's own default set, offered as the one-click action on a rule
    // sitting at off. Visible and chosen, never written behind the user.
    pdQSDefaultSet() {
      return (this.pdQSSets || []).find(
        s => s.origin === 'guide' && s.name === this.pdQSDefaultSetType
      ) || null;
    },

    // --- Rows ---

    // Default to what the profile allows, with a show-all switch. A size on a
    // quality the profile does not allow changes nothing, because a
    // disallowed quality is never downloaded. That argument only holds per
    // profile: on the shared list a size mattered if any profile allowed it.
    //
    // Allowed qualities the set does not cover still belong here, showing the
    // instance's own value, because that is exactly where someone might want
    // to set something themselves.
    pdQSVisibleRows() {
      const rows = this.pdQSRows || [];
      if (this.pdQSShowAll) return rows;
      return rows.filter(row => row.allowed || this.pdQSIsCustom(row.quality));
    },

    pdQSHiddenCount() {
      return (this.pdQSRows || []).length - this.pdQSVisibleRows().length;
    },

    // Where this row's number comes from. The user needs this to know which
    // lever to pull: a value from the app default looks identical to one
    // chosen for this profile unless the tab says so.
    pdQSRowSource(row) {
      if (this.pdQSIsCustom(row.quality)) return 'Your value';
      const set = this.pdQSActiveSet();
      if (!set) return 'Not written';
      if (!this.pdQSEntryFor(row.quality)) return 'Not in this set, left as is';
      return set.origin === 'user' ? set.name : this.pdQSSetTitle(set.name);
    },

    pdQSCurrentText(row) {
      const parts = QS_FIELDS.map(f => {
        const v = row.current?.[f];
        // The app stores no value for Unlimited, and its own UI shows the
        // word rather than a number.
        return v === null || v === undefined ? '∞' : v;
      });
      return parts.join(' / ');
    },

    // Resolved client-side in the same order the sync engine uses: an
    // override, then the chosen set, then nothing. Not read from the row's
    // resolved field, which the backend computed from the *saved* rule and
    // would show the previous set's numbers under the new set's name.
    pdQSResolvedText(row) {
      if (this.pdQSIsCustom(row.quality)) {
        const ov = this.pdQSOverrides[row.quality];
        return QS_FIELDS.map(f => ov?.[f] ?? 0).join(' / ');
      }
      const entry = this.pdQSEntryFor(row.quality);
      if (!entry) return '—';
      return QS_FIELDS.map(f => entry[f]).join(' / ');
    },

    // --- Per-quality overrides ---

    pdQSIsCustom(quality) {
      return !!(this.pdQSOverrides || {})[quality];
    },

    pdQSToggleCustom(row) {
      const next = { ...(this.pdQSOverrides || {}) };
      if (next[row.quality]) {
        delete next[row.quality];
      } else {
        // Prefill with whatever the row would get today: the chosen set's
        // entry when it has one, otherwise the instance's own values. A null
        // means the instance has no value, which is its limit rather than 0,
        // so it must not prefill 0 and silently cap everything.
        const src = this.pdQSEntryFor(row.quality) || row.current;
        const lim = this.pdQSLimits();
        next[row.quality] = {
          min: src?.min ?? 0,
          preferred: src?.preferred ?? lim.preferred,
          max: src?.max ?? lim.max,
        };
      }
      this.pdQSOverrides = next;
      this._pdQSTouched = true;
    },

    pdQSOverrideVal(quality, field) {
      return (this.pdQSOverrides || {})[quality]?.[field] ?? 0;
    },

    pdQSSetOverrideVal(quality, field, value) {
      const next = { ...(this.pdQSOverrides || {}) };
      if (!next[quality]) return;
      const n = parseFloat(value);
      next[quality] = { ...next[quality], [field]: Number.isFinite(n) ? n : 0 };
      this.pdQSOverrides = next;
      this._pdQSTouched = true;
    },

    // The app's ceilings, used when prefilling an Unlimited field. Sonarr and
    // Radarr differ, and the numbers are the same ones the instance-wide tab
    // uses.
    pdQSLimits() {
      const app = this.profileDetail?.instance?.type;
      return app === 'radarr'
        ? { preferred: 1999, max: 2000 }
        : { preferred: 995, max: 1000 };
    },

    pdQSChangeCount() {
      return Object.keys(this.pdQSOverrides || {}).length;
    },

    // What the basics-strip row shows.
    pdQSSummary() {
      const n = this.pdQSChangeCount();
      const base = this.pdQSSourceLabel();
      // An override is written even with no set chosen, so "Off" on its own
      // would understate what the next sync does.
      return n > 0 ? `${base} · ${n} change${n === 1 ? '' : 's'}` : base;
    },

    // 'off' and not '': an empty source is indistinguishable from "the field
    // was not sent", which every partial rule writer relies on, so an empty
    // one could never be saved and this button did nothing.
    pdQSReset() {
      this.pdQSSource = 'off';
      this.pdQSOverrides = {};
      this._pdQSTouched = true;
    },

    async pdQSDeleteSet(set) {
      if (!set || set.origin !== 'user' || !set.id) return;
      const ok = await new Promise(resolve => {
        this.confirmModal = {
          show: true,
          title: 'Delete this size set?',
          message: `"${set.name}" is removed from the list for every profile. Profiles already pointed at it keep the sizes they have until you choose something else.`,
          confirmLabel: 'Delete',
          onConfirm: () => resolve(true),
          onCancel: () => resolve(false),
        };
      });
      if (!ok) return;
      try {
        const r = await fetch(`/api/quality-size-sets/${set.id}`, { method: 'DELETE' });
        const d = await r.json().catch(() => ({}));
        if (!r.ok) {
          // The backend refuses while a rule still points at it and names
          // the instances, so the message is worth showing verbatim.
          this.showToast(d.error || 'Could not delete the set', 'error');
          return;
        }
        this.pdQSSets = (this.pdQSSets || []).filter(x => x.value !== set.value);
        if (this.pdQSSource === set.value) this.pdQSReset();
        this.showToast(`Deleted "${set.name}"`, 'success');
      } catch (e) {
        console.error('pdQSDeleteSet:', e);
        this.showToast('Could not delete the set', 'error');
      }
    },

    // --- Saving the current table as a reusable set ---

    // A one-time action, not a standing link: it writes out the resolved
    // table under a name and points the rule at it. Editing the set later
    // does not reach back into a rule that merely happened to be its source.
    async pdQSSaveAsSet() {
      const name = (this.pdQSSaveSetName || '').trim();
      if (!name) {
        this.showToast('Give the set a name first', 'error');
        return;
      }
      const app = this.profileDetail?.instance?.type;
      // Built from the same client-side resolution the table shows, so the
      // saved set is what the user is looking at. Reading row.resolved here
      // took the *saved* rule's numbers, which meant picking a new set and
      // saving it wrote the previous set's values under the new name.
      const qualities = [];
      for (const row of this.pdQSRows || []) {
        const v = this.pdQSIsCustom(row.quality)
          ? this.pdQSOverrides[row.quality]
          : this.pdQSEntryFor(row.quality);
        if (!v) continue;
        qualities.push({
          quality: row.quality,
          min: v.min ?? 0,
          preferred: v.preferred ?? 0,
          max: v.max ?? 0,
        });
      }
      if (qualities.length === 0) {
        this.showToast('There are no sizes to save yet', 'error');
        return;
      }
      try {
        const r = await fetch('/api/quality-size-sets', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name, appType: app, qualities }),
        });
        const d = await r.json();
        if (!r.ok) {
          this.showToast(d.error || 'Could not save the set', 'error');
          return;
        }
        // Point the rule at the new set and drop the overrides it just baked
        // in, so the same numbers are not stored in two places.
        this.pdQSSets = [...(this.pdQSSets || []), {
          value: `custom:${d.id}`, name: d.name, origin: 'user', id: d.id, entries: qualities,
        }];
        this.pdQSSource = `custom:${d.id}`;
        this.pdQSOverrides = {};
        this.pdQSSaveSetName = '';
        this._pdQSTouched = true;
        this.showToast(`Saved "${name}" and pointed this profile at it`, 'success');
      } catch (e) {
        console.error('pdQSSaveAsSet:', e);
        this.showToast('Could not save the set', 'error');
      }
    },
  },
};
