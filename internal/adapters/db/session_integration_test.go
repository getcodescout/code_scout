package db

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/getcodescout/code_scout/internal/domain"
	"github.com/google/uuid"
)

func strp(s string) *string { return &s }

// The SDK re-sends the session record with every batch, so the repeat is the
// normal case. What it must never do is move started_at — otherwise every
// session looks like it began at its most recent sync, and "how long did this
// session run" becomes meaningless.
func TestSessionUpsertKeepsTheOriginalStart(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	sessionID := uuid.New()
	started := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	t.Cleanup(func() { db.Unscoped().Where("id = ?", sessionID).Delete(&SessionModel{}) })

	first := domain.Session{
		ID: sessionID, ProjectID: projectID,
		DeviceModel: strp("Pixel 7"), OSName: strp("Android"), OSVersion: strp("14"),
		StartedAt: started, LastSeenAt: started,
	}
	if err := repo.Upsert(ctx, &first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	// A later batch, claiming a much later start.
	later := time.Now().Truncate(time.Second)
	second := first
	second.StartedAt = later
	second.LastSeenAt = later
	second.UserID = strp("u_8812")
	if err := repo.Upsert(ctx, &second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := repo.GetByID(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if !got.StartedAt.Equal(started) {
		t.Errorf("started_at moved: want %s, got %s", started, got.StartedAt)
	}
	if !got.LastSeenAt.Equal(later) {
		t.Errorf("last_seen_at should advance: want %s, got %s", later, got.LastSeenAt)
	}
	// setUser() can be called mid-session, so identity has to be revisable.
	if got.UserID == nil || *got.UserID != "u_8812" {
		t.Errorf("a later batch should be able to set the user, got %v", got.UserID)
	}

	var rows int64
	db.Model(&SessionModel{}).Where("id = ?", sessionID).Count(&rows)
	if rows != 1 {
		t.Errorf("re-sending a session should update, not duplicate: %d rows", rows)
	}
}

// The SDK version has to be revisable on a session that already exists, which
// means it has to be in the upsert's DoUpdates list.
//
// Leaving it out passes any test that uploads once. It only shows up on the
// second batch, which is exactly the case that matters: an app whose SDK was
// upgraded between two syncs of the same launch would keep reporting the old
// version forever, and the column would be lying about the thing it exists to
// answer.
func TestSessionUpsertRevisesTheSDKVersion(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	sessionID := uuid.New()
	now := time.Now().Truncate(time.Second)
	t.Cleanup(func() { db.Unscoped().Where("id = ?", sessionID).Delete(&SessionModel{}) })

	first := domain.Session{
		ID: sessionID, ProjectID: projectID,
		SDKVersion: strp("1.3.1"),
		StartedAt:  now, LastSeenAt: now,
	}
	if err := repo.Upsert(ctx, &first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}

	second := first
	second.SDKVersion = strp("1.4.0")
	if err := repo.Upsert(ctx, &second); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	got, err := repo.GetByID(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SDKVersion == nil {
		t.Fatal("sdk_version came back nil after the second batch")
	}
	if *got.SDKVersion != "1.4.0" {
		t.Errorf("sdk_version did not follow the second batch: got %q, want 1.4.0", *got.SDKVersion)
	}
}

// An SDK older than the one that started sending it leaves the field out
// entirely, which is a null column and not a reason to reject the session.
func TestSessionWithoutAnSDKVersionIsStillRecorded(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	sessionID := uuid.New()
	now := time.Now().Truncate(time.Second)
	t.Cleanup(func() { db.Unscoped().Where("id = ?", sessionID).Delete(&SessionModel{}) })

	s := domain.Session{ID: sessionID, ProjectID: projectID, StartedAt: now, LastSeenAt: now}
	if err := repo.Upsert(ctx, &s); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	got, err := repo.GetByID(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SDKVersion != nil {
		t.Errorf("sdk_version = %v, want nil", *got.SDKVersion)
	}
}

// varchar(64) with a longer value is a failed INSERT, and the session is
// dropped whole rather than the field being trimmed. That has happened here
// before, with os_version, and it cost entire sessions silently. clamp is the
// answer and it has to be applied to every string column, including new ones.
func TestSessionSDKVersionIsClamped(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	sessionID := uuid.New()
	now := time.Now().Truncate(time.Second)
	t.Cleanup(func() { db.Unscoped().Where("id = ?", sessionID).Delete(&SessionModel{}) })

	long := strings.Repeat("9", 200)
	s := domain.Session{
		ID: sessionID, ProjectID: projectID,
		SDKVersion: &long,
		StartedAt:  now, LastSeenAt: now,
	}
	if err := repo.Upsert(ctx, &s); err != nil {
		t.Fatalf("an over-long sdk_version took the whole session down: %v", err)
	}

	got, err := repo.GetByID(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.SDKVersion == nil {
		t.Fatal("sdk_version was dropped entirely")
	}
	if n := utf8.RuneCountInString(*got.SDKVersion); n != 64 {
		t.Errorf("sdk_version stored %d characters, want 64", n)
	}
}

// The project comes from the authenticated headers. Re-sending a session with
// a different project must not move it, or a client could write into a project
// it has credentials for and then read it from one it does not.
func TestSessionUpsertCannotMoveProject(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	mine := seedProject(t, db)
	theirs := seedProject(t, db)

	sessionID := uuid.New()
	t.Cleanup(func() { db.Unscoped().Where("id = ?", sessionID).Delete(&SessionModel{}) })

	now := time.Now()
	original := domain.Session{ID: sessionID, ProjectID: mine, StartedAt: now, LastSeenAt: now}
	if err := repo.Upsert(ctx, &original); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	moved := original
	moved.ProjectID = theirs
	if err := repo.Upsert(ctx, &moved); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	if _, err := repo.GetByID(ctx, mine, sessionID); err != nil {
		t.Errorf("the session should still belong to its original project: %v", err)
	}
	if _, err := repo.GetByID(ctx, theirs, sessionID); err == nil {
		t.Error("the session must not have moved to another project")
	}
}

// A session read must be scoped, so an id guessed from another project is a
// miss rather than a disclosure.
func TestSessionGetIsProjectScoped(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	mine := seedProject(t, db)
	theirs := seedProject(t, db)

	sessionID := uuid.New()
	t.Cleanup(func() { db.Unscoped().Where("id = ?", sessionID).Delete(&SessionModel{}) })

	now := time.Now()
	s := domain.Session{ID: sessionID, ProjectID: mine, StartedAt: now, LastSeenAt: now}
	if err := repo.Upsert(ctx, &s); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	if _, err := repo.GetByID(ctx, theirs, sessionID); err == nil {
		t.Error("another project should not be able to read this session")
	}
}

// Display labels are what the sessions and devices lists render, and a missing
// user is a real answer rather than a blank.
func TestSessionLabels(t *testing.T) {
	full := domain.Session{
		DeviceModel: strp("Pixel 7"), OSName: strp("Android"), OSVersion: strp("14"),
		UserID: strp("u_8812"),
	}
	if got := full.DeviceLabel(); got != "Pixel 7 · Android 14" {
		t.Errorf("got %q", got)
	}
	if got := full.UserLabel(); got != "u_8812" {
		t.Errorf("got %q", got)
	}
	if !full.IsIdentified() {
		t.Error("a session with a user id is identified")
	}

	bare := domain.Session{}
	if got := bare.DeviceLabel(); got != "Unknown device" {
		t.Errorf("got %q", got)
	}
	if got := bare.UserLabel(); got != "anonymous" {
		t.Errorf("identity is opt-in, so an unset user reads as anonymous, got %q", got)
	}
	if bare.IsIdentified() {
		t.Error("a session with no user id is not identified")
	}
}

// seedSession stores one launch. Everything the list screens show comes from
// here, so the helper takes exactly the fields those screens read.
func seedSession(t *testing.T, repo *SessionRepo, projectID uuid.UUID, installID *uuid.UUID, user *string, model string, started time.Time, ran time.Duration) uuid.UUID {
	t.Helper()
	id := uuid.New()
	err := repo.Upsert(context.Background(), &domain.Session{
		ID: id, ProjectID: projectID,
		InstallationID: installID, UserID: user,
		DeviceModel: strp(model), OSName: strp("Android"), OSVersion: strp("14"),
		AppVersion: strp("3.11.2"), BuildNumber: strp("418"),
		StartedAt: started, LastSeenAt: started.Add(ran),
	})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return id
}

// The counts beside a session come from its logs, not from the session record,
// so they stay right however late a batch lands.
func TestListSessionsCountsItsLogs(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	logs := NewLogRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	busy := seedSession(t, repo, projectID, nil, strp("u_8812"), "Pixel 7", base, 5*time.Minute)
	quiet := seedSession(t, repo, projectID, nil, nil, "iPhone 15 Pro", base.Add(-time.Hour), time.Minute)

	netLog := func(sessionID uuid.UUID) domain.Log {
		l := taggedLog(projectID, "GET /v2/cart", "debug", nil)
		l.SessionID = sessionID
		l.IsNetworkCall = true
		return l
	}
	withSession := func(l domain.Log, sessionID uuid.UUID) domain.Log {
		l.SessionID = sessionID
		return l
	}
	if _, err := logs.CreateBatch(ctx, []domain.Log{
		withSession(taggedLog(projectID, "one", "info", nil), busy),
		withSession(taggedLog(projectID, "two", "error", nil), busy),
		withSession(taggedLog(projectID, "three", "fatal", nil), busy),
		netLog(busy),
		withSession(taggedLog(projectID, "elsewhere", "info", nil), quiet),
	}); err != nil {
		t.Fatalf("seed logs: %v", err)
	}

	out, err := repo.List(ctx, projectID, nil, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(out))
	}

	// Newest first.
	if out[0].ID != busy {
		t.Fatalf("want the newest session first, got %s", out[0].ID)
	}
	if out[0].Logs != 4 {
		t.Errorf("want 4 logs, got %d", out[0].Logs)
	}
	// Fatal counts as an error. A fatal that did not would be the one thing you
	// most needed to see, missing.
	if out[0].Errors != 2 {
		t.Errorf("want 2 errors (error + fatal), got %d", out[0].Errors)
	}
	if out[0].Network != 1 {
		t.Errorf("want 1 network call, got %d", out[0].Network)
	}
	if out[0].Duration() != 5*time.Minute {
		t.Errorf("want a 5m session, got %s", out[0].Duration())
	}
	if !out[0].IsIdentified() || out[1].IsIdentified() {
		t.Errorf("identity is opt-in: want the first identified and the second not")
	}
}

// A log the app wrote about a call is a log, and an error when it is one, but
// not network traffic: the network count is the phase rows alone.
func TestSessionCountsTakeALinkedLogAsALogNotACall(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	logs := NewLogRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	launch := seedSession(t, repo, projectID, nil, nil, "Pixel 7", base, time.Minute)
	cart := uuid.New()
	if _, err := logs.CreateBatch(ctx, []domain.Log{
		netPhase(projectID, launch, cart, "request", "GET", "https://api.test/v2/cart", nil, base),
		netPhase(projectID, launch, cart, "response", "GET", "https://api.test/v2/cart", intp(200), base.Add(time.Second)),
		linkedAppLog(projectID, launch, cart, base.Add(2*time.Second)),
	}); err != nil {
		t.Fatalf("seed logs: %v", err)
	}

	out, err := repo.List(ctx, projectID, nil, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("want 1 session, got %d", len(out))
	}
	if out[0].Logs != 3 || out[0].Errors != 1 || out[0].Network != 2 {
		t.Errorf("want 3 logs, 1 error and 2 network, got %d, %d and %d", out[0].Logs, out[0].Errors, out[0].Network)
	}
}

// A session that has logged nothing is still a session — the app launched. It
// must not vanish because an inner join found no logs to match.
func TestListSessionsKeepsSilentOnes(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	seedSession(t, repo, projectID, nil, nil, "Pixel 6a", time.Now().Add(-time.Minute), time.Minute)

	out, err := repo.List(ctx, projectID, nil, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("a session with no logs should still be listed, got %d", len(out))
	}
	if out[0].Logs != 0 || out[0].Errors != 0 {
		t.Errorf("want zero counts, got %d logs and %d errors", out[0].Logs, out[0].Errors)
	}
}

// One phone across many launches is one row. That is the whole point of the
// Devices screen, and it is what the installation id is for.
func TestListDevicesRollsUpItsSessions(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	logs := NewLogRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	phone := uuid.New()
	other := uuid.New()
	base := time.Now().Add(-48 * time.Hour).Truncate(time.Second)

	// Three launches of the same phone. The oldest had a user, the newest did
	// not, and the model changed between them.
	first := seedSession(t, repo, projectID, &phone, strp("u_8812"), "Pixel 7", base, time.Minute)
	seedSession(t, repo, projectID, &phone, nil, "Pixel 7", base.Add(time.Hour), time.Minute)
	seedSession(t, repo, projectID, &phone, nil, "Pixel 7 Pro", base.Add(2*time.Hour), time.Minute)
	seedSession(t, repo, projectID, &other, nil, "iPhone 15 Pro", base.Add(3*time.Hour), time.Minute)

	l := taggedLog(projectID, "went wrong", "error", nil)
	l.SessionID = first
	if _, err := logs.CreateBatch(ctx, []domain.Log{l}); err != nil {
		t.Fatalf("seed logs: %v", err)
	}

	devices, err := repo.ListDevices(ctx, projectID, 50)
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("want 2 devices, got %d", len(devices))
	}

	var got *domain.Device
	for i := range devices {
		if devices[i].InstallationID == phone {
			got = &devices[i]
		}
	}
	if got == nil {
		t.Fatal("the phone is missing from the device list")
	}
	if got.Sessions != 3 {
		t.Errorf("want 3 launches rolled into one row, got %d", got.Sessions)
	}
	if got.Errors != 1 {
		t.Errorf("errors should sum across the install's sessions, got %d", got.Errors)
	}
	// The newest launch's answer, so an updated phone reads as what it is now.
	if got.DeviceModel == nil || *got.DeviceModel != "Pixel 7 Pro" {
		t.Errorf("want the most recent model, got %v", got.DeviceModel)
	}
	// The most recent launch that actually had a user. Two anonymous launches
	// since should not make the device forget who was on it.
	if got.LastUserID == nil || *got.LastUserID != "u_8812" {
		t.Errorf("want the last known user, got %v", got.LastUserID)
	}
	if !got.FirstSeen.Equal(base) {
		t.Errorf("want first seen %s, got %s", base, got.FirstSeen)
	}
}

// Sessions with no installation id are skipped rather than collapsed into one
// nameless row, which would claim several phones are one.
func TestListDevicesSkipsSessionsWithNoInstall(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	seedSession(t, repo, projectID, nil, nil, "Pixel 7", time.Now().Add(-time.Hour), time.Minute)
	seedSession(t, repo, projectID, nil, nil, "iPhone 13", time.Now().Add(-time.Minute), time.Minute)

	devices, err := repo.ListDevices(ctx, projectID, 50)
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	if len(devices) != 0 {
		t.Errorf("sessions with no install id should not become devices, got %d", len(devices))
	}
}

// The device detail screen lists only that phone's launches.
func TestListSessionsNarrowsToOneInstall(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	phone, other := uuid.New(), uuid.New()
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	seedSession(t, repo, projectID, &phone, nil, "Pixel 7", base, time.Minute)
	seedSession(t, repo, projectID, &phone, nil, "Pixel 7", base.Add(time.Minute), time.Minute)
	seedSession(t, repo, projectID, &other, nil, "iPhone 13", base.Add(2*time.Minute), time.Minute)

	out, err := repo.List(ctx, projectID, &phone, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("want only the phone's 2 launches, got %d", len(out))
	}
	for _, s := range out {
		if s.InstallationID == nil || *s.InstallationID != phone {
			t.Errorf("another device's session leaked in: %+v", s.InstallationID)
		}
	}
}

// Counts are about everything, not about the capped list, and an anonymous
// launch is not a user.
func TestSessionCountsIgnoreAnonymousLaunches(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	base := time.Now().Add(-time.Hour)
	seedSession(t, repo, projectID, nil, strp("u_1"), "Pixel 7", base, time.Minute)
	seedSession(t, repo, projectID, nil, strp("u_1"), "Pixel 7", base.Add(time.Minute), time.Minute)
	seedSession(t, repo, projectID, nil, strp("u_2"), "Pixel 7", base.Add(2*time.Minute), time.Minute)
	seedSession(t, repo, projectID, nil, nil, "Pixel 7", base.Add(3*time.Minute), time.Minute)

	sessions, users, err := repo.Counts(ctx, projectID)
	if err != nil {
		t.Fatalf("counts: %v", err)
	}
	if sessions != 4 {
		t.Errorf("want 4 sessions, got %d", sessions)
	}
	if users != 2 {
		t.Errorf("want 2 distinct users, got %d", users)
	}
}

// Another project's launches never appear.
func TestSessionsAreProjectScoped(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	mine := seedProject(t, db)
	theirs := seedProject(t, db)

	phone := uuid.New()
	seedSession(t, repo, mine, &phone, nil, "Mine", time.Now().Add(-time.Minute), time.Minute)
	seedSession(t, repo, theirs, &phone, nil, "Theirs", time.Now(), time.Minute)

	out, err := repo.List(ctx, mine, nil, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(out) != 1 || *out[0].DeviceModel != "Mine" {
		t.Errorf("another project's sessions leaked in: %+v", out)
	}

	// The same install id in two projects is two devices, not one.
	devices, err := repo.ListDevices(ctx, mine, 50)
	if err != nil {
		t.Fatalf("devices: %v", err)
	}
	if len(devices) != 1 || devices[0].Sessions != 1 {
		t.Errorf("device rollup crossed a project boundary: %+v", devices)
	}
}

// A Linux launch reports an OS version like
// "#26~22.04.1-Ubuntu SMP PREEMPT_DYNAMIC Thu Jul 11 22:33:04 UTC 2024" where
// macOS reports "Version 26.5.2 (Build 25F84)". The long one overflowed
// varchar(64) and the whole session was lost — silently, because a failed
// session upsert deliberately does not fail the upload carrying it. The logs
// arrived and the user, device and app version they belonged to did not.
func TestSessionUpsertSurvivesAnOverlongOSVersion(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	sessionID := uuid.New()
	t.Cleanup(func() { db.Unscoped().Where("id = ?", sessionID).Delete(&SessionModel{}) })

	long := "#26~22.04.1-Ubuntu SMP PREEMPT_DYNAMIC Thu Jul 11 22:33:04 UTC 2024"
	if len(long) <= 64 {
		t.Fatalf("the fixture has to exceed the column, got %d chars", len(long))
	}

	session := domain.Session{
		ID: sessionID, ProjectID: projectID,
		UserID:     strp("someone"),
		OSName:     strp("Linux"),
		OSVersion:  strp(long),
		AppVersion: strp("9.9.9"),
		StartedAt:  time.Now(),
		LastSeenAt: time.Now(),
	}
	if err := repo.Upsert(ctx, &session); err != nil {
		t.Fatalf("an overlong OS version must not lose the session: %v", err)
	}

	stored, err := repo.GetByID(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	// The fields that matter survived whole; only the descriptive tail is cut.
	if stored.UserID == nil || *stored.UserID != "someone" {
		t.Errorf("the user was lost: %v", stored.UserID)
	}
	if stored.AppVersion == nil || *stored.AppVersion != "9.9.9" {
		t.Errorf("the app version was lost: %v", stored.AppVersion)
	}
	if stored.OSVersion == nil || len([]rune(*stored.OSVersion)) != 64 {
		t.Errorf("want the OS version cut to 64 characters, got %v", stored.OSVersion)
	}
	if stored.OSVersion != nil && !strings.HasPrefix(long, *stored.OSVersion) {
		t.Errorf("the kept part should be the front of the original, got %q", *stored.OSVersion)
	}
}

// Multi-byte text must not be cut mid-rune: Postgres counts characters, so
// cutting on bytes would store broken UTF-8 for a limit that was never reached.
func TestSessionUpsertCutsOnCharactersNotBytes(t *testing.T) {
	db := testDB(t)
	repo := NewSessionRepo(db)
	ctx := context.Background()
	projectID := seedProject(t, db)

	sessionID := uuid.New()
	t.Cleanup(func() { db.Unscoped().Where("id = ?", sessionID).Delete(&SessionModel{}) })

	// 80 three-byte characters: 80 runes, 240 bytes.
	model := strings.Repeat("設", 80)
	session := domain.Session{
		ID: sessionID, ProjectID: projectID,
		DeviceModel: strp(model),
		StartedAt:   time.Now(), LastSeenAt: time.Now(),
	}
	if err := repo.Upsert(ctx, &session); err != nil {
		t.Fatalf("a multi-byte device name must not lose the session: %v", err)
	}

	stored, err := repo.GetByID(ctx, projectID, sessionID)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if stored.DeviceModel == nil || !utf8.ValidString(*stored.DeviceModel) {
		t.Fatalf("the stored device model is not valid UTF-8: %v", stored.DeviceModel)
	}
	// 80 runes is under the 255-character limit, so nothing should be cut at
	// all — it only looks long if you count bytes.
	if *stored.DeviceModel != model {
		t.Errorf("a 240-byte, 80-character name was cut when it fits: %q", *stored.DeviceModel)
	}
}
