package session

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// fakeRunner records the remote command strings and returns canned output. It
// is the fake-runner seam PLAN §3 requires: every ssh/tmux string is asserted
// here, no process ever touches a real host.
type fakeRunner struct {
	cmds []string
	out  map[string]string // exact-cmd → stdout
	err  error
}

func (f *fakeRunner) Run(cmd string) (string, error) {
	f.cmds = append(f.cmds, cmd)
	if f.err != nil {
		return "", f.err
	}
	if f.out != nil {
		if v, ok := f.out[cmd]; ok {
			return v, nil
		}
	}
	return "", nil
}

func (f *fakeRunner) RunInput(cmd string, _ io.Reader) (string, error) {
	f.cmds = append(f.cmds, cmd)
	return "", f.err
}

// fakeAttacher records the attached session id.
type fakeAttacher struct {
	attached string
	waited   string
}

func (f *fakeAttacher) ExecAttach(id string) error { f.attached = id; return nil }
func (f *fakeAttacher) RunAttach(id string) error  { f.waited = id; return nil }

func last(f *fakeRunner) string { return f.cmds[len(f.cmds)-1] }

func TestListBuildsFormatStringAndParses(t *testing.T) {
	f := &fakeRunner{out: map[string]string{}}
	// Build the exact command the manager should issue, then seed its output.
	want := "tmux list-sessions -F '" + listFormat + "'"
	f.out[want] = "foo\t~/dev/foo\t1\t1700000000\t2\nbar\t~/dev/bar\t0\t1700000100\t1\n"
	m := NewManager(f, &fakeAttacher{})

	sessions, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if last(f) != want {
		t.Fatalf("List command =\n  %q\nwant\n  %q", last(f), want)
	}
	// The format must NOT contain a literal ./: — and must request @duck_dir.
	if !strings.Contains(listFormat, "@duck_dir") {
		t.Fatalf("list format must read @duck_dir: %q", listFormat)
	}
	if len(sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sessions))
	}
	if sessions[0].Name != "foo" || sessions[0].Dir != "~/dev/foo" || !sessions[0].Attached || sessions[0].Windows != 2 {
		t.Fatalf("parsed row 0 wrong: %+v", sessions[0])
	}
	if sessions[1].Attached {
		t.Fatalf("row 1 has session_attached=0; should be detached: %+v", sessions[1])
	}
}

// TestListParsesLoopAndPaneTitle pins the two trailing fields: @duck_loop (the
// looped-session marker the picker pins on) then pane_title. A "1" loop value
// reads as Looped=true; an empty/"0" value reads as false; a 5-field line (no
// trailing fields at all) leaves both zero so the feature degrades to a no-op.
func TestListParsesLoopAndPaneTitle(t *testing.T) {
	f := &fakeRunner{out: map[string]string{}}
	want := "tmux list-sessions -F '" + listFormat + "'"
	f.out[want] = "lp\t~/dev/lp\t0\t100\t1\t1\t✳ working\n" + // looped, with a Claude title
		"plain\t~/dev/plain\t0\t100\t1\t\tshell\n" + // empty loop field → not looped
		"old\t~/dev/old\t0\t100\t1\n" // 5-field legacy line → both zero
	m := NewManager(f, &fakeAttacher{})

	sessions, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(sessions) != 3 {
		t.Fatalf("want 3 sessions, got %d: %+v", len(sessions), sessions)
	}
	if !sessions[0].Looped || sessions[0].PaneTitle != "✳ working" {
		t.Fatalf("row 0 should be looped with pane title: %+v", sessions[0])
	}
	if sessions[1].Looped || sessions[1].PaneTitle != "shell" {
		t.Fatalf("row 1 empty loop field should be not-looped: %+v", sessions[1])
	}
	if sessions[2].Looped || sessions[2].PaneTitle != "" {
		t.Fatalf("row 2 legacy 5-field line should leave loop/title zero: %+v", sessions[2])
	}
}

func TestListNoServerIsEmptyNotError(t *testing.T) {
	// tmux exits non-zero with "no server running on <socket>" on an empty hub;
	// List must absorb THAT signature into an empty slice, not an error. sshx
	// folds the remote stderr into the returned error, so the fake surfaces it
	// through err.
	f := &fakeRunner{err: errors.New("tmux list-sessions: exit status 1: no server running on /tmp/tmux-1000/default")}
	m := NewManager(f, &fakeAttacher{})
	got, err := m.List()
	if err != nil {
		t.Fatalf("List on no-server should be nil error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty slice, got %+v", got)
	}
}

func TestListNeverStartedSocketIsEmptyNotError(t *testing.T) {
	// On a box where tmux has NEVER started the socket file is absent, so tmux
	// emits "error connecting to <socket> (No such file or directory)" instead of
	// "no server running". List must absorb THAT signature too — otherwise a bare
	// `duck` on a fresh machine fails instead of starting the first session.
	f := &fakeRunner{err: errors.New("tmux list-sessions: exit status 1: error connecting to /private/tmp/tmux-501/default (No such file or directory)")}
	m := NewManager(f, &fakeAttacher{})
	got, err := m.List()
	if err != nil {
		t.Fatalf("List on never-started socket should be nil error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want empty slice, got %+v", got)
	}
}

// TestListPropagatesTransportError pins invariant: a real transport/ssh failure
// (hub down, auth rejected) is NOT the empty-server case and must PROPAGATE, so
// `duck clean` cannot silently report "no detached sessions" on a dead hub.
func TestListPropagatesTransportError(t *testing.T) {
	wantErr := errors.New("ssh: connect to host hub port 22: Connection refused")
	f := &fakeRunner{err: wantErr}
	m := NewManager(f, &fakeAttacher{})
	got, err := m.List()
	if err == nil {
		t.Fatalf("List must propagate a non-no-server error, got nil (rows=%+v)", got)
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("List should return the underlying transport error, got %v", err)
	}
}

func TestNewSessionUsesHubPathAndStampsDuckDir(t *testing.T) {
	f := &fakeRunner{}
	m := NewManager(f, &fakeAttacher{})
	if err := m.New("foo", "~/dev/foo"); err != nil {
		t.Fatalf("New: %v", err)
	}
	if len(f.cmds) != 2 {
		t.Fatalf("New should issue new-session then set-option, got %v", f.cmds)
	}
	// new-session uses -c with a $HOME-expanded path (tmux -c does NOT expand ~).
	wantNew := `tmux new-session -d -s 'foo' -c "$HOME"/'dev/foo'`
	if f.cmds[0] != wantNew {
		t.Fatalf("new-session =\n  %q\nwant\n  %q", f.cmds[0], wantNew)
	}
	// @duck_dir is stamped with the RAW tilde-form dir (the Recent/display key).
	wantOpt := `tmux set-option -t 'foo' '@duck_dir' '~/dev/foo'`
	if f.cmds[1] != wantOpt {
		t.Fatalf("set-option =\n  %q\nwant\n  %q", f.cmds[1], wantOpt)
	}
}

func TestDirExistsProbesHubPath(t *testing.T) {
	// A tilde dir → $HOME-expanded test -d probe; "yes\n" means exists.
	wantCmd := `test -d "$HOME"/'dev/foo' && echo yes || echo no`
	f := &fakeRunner{out: map[string]string{wantCmd: "yes\n"}}
	m := NewManager(f, &fakeAttacher{})

	ok, err := m.DirExists("~/dev/foo")
	if err != nil {
		t.Fatalf("DirExists: %v", err)
	}
	if last(f) != wantCmd {
		t.Fatalf("DirExists cmd =\n  %q\nwant\n  %q", last(f), wantCmd)
	}
	if !ok {
		t.Fatalf("DirExists should report true for a 'yes' probe")
	}

	// "no\n" means it does not exist; home maps to "$HOME".
	wantHome := `test -d "$HOME" && echo yes || echo no`
	f2 := &fakeRunner{out: map[string]string{wantHome: "no\n"}}
	m2 := NewManager(f2, &fakeAttacher{})
	ok2, err := m2.DirExists("~")
	if err != nil {
		t.Fatalf("DirExists: %v", err)
	}
	if last(f2) != wantHome {
		t.Fatalf("DirExists(~) cmd =\n  %q\nwant\n  %q", last(f2), wantHome)
	}
	if ok2 {
		t.Fatalf("DirExists should report false for a 'no' probe")
	}
}

func TestKillAndOptionAndSetOptionStrings(t *testing.T) {
	f := &fakeRunner{out: map[string]string{
		"tmux show-options -t 'foo' -v '@duck_dir'": "~/dev/foo\n",
	}}
	m := NewManager(f, &fakeAttacher{})

	if err := m.Kill("foo"); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if last(f) != "tmux kill-session -t 'foo'" {
		t.Fatalf("Kill = %q", last(f))
	}

	val, ok, err := m.Option("foo", "@duck_dir")
	if err != nil || !ok || val != "~/dev/foo" {
		t.Fatalf("Option = %q ok=%v err=%v", val, ok, err)
	}
	if last(f) != "tmux show-options -t 'foo' -v '@duck_dir'" {
		t.Fatalf("Option cmd = %q", last(f))
	}

	if err := m.SetOption("foo", "@duck_dir", "~/dev/foo"); err != nil {
		t.Fatalf("SetOption: %v", err)
	}
	if last(f) != `tmux set-option -t 'foo' '@duck_dir' '~/dev/foo'` {
		t.Fatalf("SetOption cmd = %q", last(f))
	}
}

func TestRecentPicksMostRecentForDir(t *testing.T) {
	want := "tmux list-sessions -F '" + listFormat + "'"
	f := &fakeRunner{out: map[string]string{
		want: "a\t~/dev/foo\t0\t100\t1\n" +
			"b\t~/dev/foo\t0\t300\t1\n" + // most recent in ~/dev/foo
			"c\t~/dev/bar\t0\t999\t1\n",
	}}
	m := NewManager(f, &fakeAttacher{})
	s, ok, err := m.Recent("~/dev/foo")
	if err != nil || !ok {
		t.Fatalf("Recent ok=%v err=%v", ok, err)
	}
	if s.Name != "b" {
		t.Fatalf("Recent picked %q, want b (max activity in ~/dev/foo)", s.Name)
	}
	// A dir with no sessions → ok=false.
	if _, ok, _ := m.Recent("~/dev/nope"); ok {
		t.Fatalf("Recent for an unknown dir should be ok=false")
	}
}

func TestAttachDelegates(t *testing.T) {
	a := &fakeAttacher{}
	m := NewManager(&fakeRunner{}, a)
	if err := m.Attach("foo-2"); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	if a.attached != "foo-2" {
		t.Fatalf("Attach delegated %q, want foo-2", a.attached)
	}
}

func TestDeriveIDAvoidsDotAndColon(t *testing.T) {
	cases := map[string]string{
		"~/dev/foo":     "foo",
		"~/dev/foo.bar": "foo-bar", // '.' is tmux-illegal → '-'
		"~/dev/a:b":     "a-b",     // ':' is tmux-illegal → '-'
		"~/dev/My Proj": "My-Proj", // space → '-'
		"~/dev/v1.2.3":  "v1-2-3",  // multiple dots
		"~":             "duck",    // degenerate base
		"~/dev/...":     "duck",    // all-illegal trims to empty → fallback
	}
	for dir, want := range cases {
		got := DeriveID(dir)
		if got != want {
			t.Errorf("DeriveID(%q) = %q, want %q", dir, got, want)
		}
		if strings.ContainsAny(got, ".:") {
			t.Errorf("DeriveID(%q) = %q contains a tmux-illegal char", dir, got)
		}
	}
}

// untouchedQuery is the exact remote command IsUntouched issues for id, so the
// tests can both assert the built query string AND seed its canned output.
func untouchedQuery(id string) string {
	return "tmux display-message -p -t '" + id + "' '" + untouchedFormat + "'"
}

// TestIsUntouchedBuildsQueryAndIsTrueForFreshShell pins the exact tmux
// display-message query AND that a [1 window, 1 pane, login shell, history 0]
// session reads as untouched.
func TestIsUntouchedBuildsQueryAndIsTrueForFreshShell(t *testing.T) {
	q := untouchedQuery("foo")
	r := &fakeRunner{out: map[string]string{q: "1|1|zsh|0\n"}}
	m := NewManager(r, &fakeAttacher{})

	got, err := m.IsUntouched("foo")
	if err != nil {
		t.Fatalf("IsUntouched: %v", err)
	}
	if !got {
		t.Fatalf("a fresh 1-window/1-pane/zsh/history-0 session must read untouched")
	}
	var sawQuery bool
	for _, c := range r.cmds {
		if c == q {
			sawQuery = true
		}
	}
	if !sawQuery {
		t.Fatalf("IsUntouched must issue the exact display-message query %q; cmds=%v", q, r.cmds)
	}
}

// TestIsUntouchedFalseWhenTouched covers every "the user worked in it" signal:
// a 2nd window, a 2nd pane, a non-shell current command (a launched program),
// or a non-empty scrollback. Any one makes the session NOT untouched.
func TestIsUntouchedFalseWhenTouched(t *testing.T) {
	cases := []struct {
		name string
		out  string
	}{
		{name: "two windows", out: "2|1|zsh|0\n"},
		{name: "two panes", out: "1|2|zsh|0\n"},
		{name: "program launched (non-shell command)", out: "1|1|claude|0\n"},
		{name: "scrolled / ran something (history>0)", out: "1|1|zsh|5\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := untouchedQuery("foo")
			r := &fakeRunner{out: map[string]string{q: tc.out}}
			m := NewManager(r, &fakeAttacher{})
			got, err := m.IsUntouched("foo")
			if err != nil {
				t.Fatalf("IsUntouched: %v", err)
			}
			if got {
				t.Fatalf("%s must read as TOUCHED (not untouched)", tc.name)
			}
		})
	}
}

// TestIsUntouchedGoneSessionIsFalseNil folds a vanished session (the user exited
// the shell, so tmux already killed it) into (false, nil): nothing to clean up.
func TestIsUntouchedGoneSessionIsFalseNil(t *testing.T) {
	r := &fakeRunner{err: errors.New("can't find session: foo")}
	m := NewManager(r, &fakeAttacher{})
	got, err := m.IsUntouched("foo")
	if err != nil {
		t.Fatalf("a gone session must be (false, nil), got err %v", err)
	}
	if got {
		t.Fatalf("a gone session is not untouched (nothing to clean up)")
	}
}

// TestAttachAndWaitUsesRunAttach pins that the fresh-path attach goes through
// the subprocess (RunAttach) variant, not ExecAttach.
func TestAttachAndWaitUsesRunAttach(t *testing.T) {
	a := &fakeAttacher{}
	m := NewManager(&fakeRunner{}, a)
	if err := m.AttachAndWait("foo-2"); err != nil {
		t.Fatalf("AttachAndWait: %v", err)
	}
	if a.waited != "foo-2" {
		t.Fatalf("AttachAndWait must RunAttach foo-2, got waited=%q", a.waited)
	}
	if a.attached != "" {
		t.Fatalf("AttachAndWait must NOT ExecAttach")
	}
}

// TestHasSession reports liveness over List: present → true, absent/empty → false.
func TestHasSession(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"tmux list-sessions -F '" + listFormat + "'": "foo\t~/dev/foo\t0\t100\t1\n",
	}}
	m := NewManager(r, &fakeAttacher{})
	if ok, err := m.HasSession("foo"); err != nil || !ok {
		t.Fatalf("HasSession(foo) = %v,%v, want true,nil", ok, err)
	}
	if ok, err := m.HasSession("bar"); err != nil || ok {
		t.Fatalf("HasSession(bar) = %v,%v, want false,nil", ok, err)
	}
}

// TestHasSessionEmptyHub: a no-server hub yields false with no error.
func TestHasSessionEmptyHub(t *testing.T) {
	r := &fakeRunner{out: map[string]string{
		"tmux list-sessions -F '" + listFormat + "'": "no server running on /tmp/x",
	}, err: errors.New("no server running on /tmp/x")}
	m := NewManager(r, &fakeAttacher{})
	if ok, err := m.HasSession("foo"); err != nil || ok {
		t.Fatalf("HasSession on empty hub = %v,%v, want false,nil", ok, err)
	}
}
