package doctor

// ProjectChecks reports, when doctor runs in a project's folder (one with
// manifestPath), whether urbex.yaml is committed in its git repository:
// apply creates the project's repo on Forgejo from it. committed is
// projectrepo.Check's answer. A project already on Forgejo doesn't need
// it any more, so it is a warning.
func ProjectChecks(manifestPath string, present bool, committed error, r *Report) {
	if !present {
		return
	}
	c := Check{Group: "project", Name: manifestPath, Status: OK, Detail: "committed in a git repository"}
	if committed != nil {
		c.Status, c.Detail = Warn, committed.Error()
		c.Fix = "needed until the project is on Forgejo: the first 'urbex apply' creates its repo there from this one"
	}
	r.add(c)
}
