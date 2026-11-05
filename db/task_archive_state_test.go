package db

import (
	"errors"
	"testing"
)

func TestTaskArchiveStaleWorkerCannotOverwriteReady(t *testing.T) {
	d, err := Open(testDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	task, err := d.CreateTaskWithOptions("archive state fence", "stale worker isolation", TaskCreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = d.Exec(`DELETE FROM task_archives WHERE task_id=$1`, task.ID)
		_ = d.DeleteTask(task.ID)
	}()
	if _, err = d.Exec(`UPDATE tasks SET paused=true WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	job, err := d.QueueTaskArchive(task.ID)
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := d.ClaimTaskArchiveJob(t.Context())
	if err != nil || claimed == nil || claimed.ID != job.ID {
		t.Fatalf("claim archive job: %+v %v", claimed, err)
	}
	if _, err = d.Exec(`UPDATE task_archives SET state='ready',phase='ready',progress=100,error='' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	if err = d.UpdateTaskArchiveProgress(job.ID, "stale snapshot", 40); err != nil {
		t.Fatal(err)
	}
	if err = d.FailTaskArchiveJob(job.ID, Archiving, errors.New("stale worker failed")); err != nil {
		t.Fatal(err)
	}
	var state, phase, message string
	var progress int
	if err = d.QueryRow(`SELECT state,phase,progress,error FROM task_archives WHERE id=$1`, job.ID).Scan(&state, &phase, &progress, &message); err != nil {
		t.Fatal(err)
	}
	if state != ArchiveReady || phase != "ready" || progress != 100 || message != "" {
		t.Fatalf("stale worker overwrote ready archive: state=%s phase=%s progress=%d error=%q", state, phase, progress, message)
	}
}
