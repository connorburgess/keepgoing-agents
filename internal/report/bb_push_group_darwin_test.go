//go:build darwin

package report

import (
	"encoding/binary"
	"os/exec"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

func groupFixture(pid, group int32, state byte) []byte {
	var layout unix.KinfoProc
	b := make([]byte, unix.SizeofKinfoProc)
	binary.LittleEndian.PutUint32(b[unsafe.Offsetof(layout.Proc)+unsafe.Offsetof(layout.Proc.P_pid):], uint32(pid))
	binary.LittleEndian.PutUint32(b[unsafe.Offsetof(layout.Eproc)+unsafe.Offsetof(layout.Eproc.Pgid):], uint32(group))
	b[unsafe.Offsetof(layout.Proc)+unsafe.Offsetof(layout.Proc.P_stat)] = state
	return b
}

func TestBBPushGroupSnapshotValidation(t *testing.T) {
	for _, state := range []byte{1, 2, 3, 4, 5} {
		b := append(groupFixture(101, 101, 5), groupFixture(102, 101, state)...)
		drained, err := bbPushDecodeGroup(b, 101)
		if err != nil || drained != (state == 5) {
			t.Fatalf("state %d: %v %v", state, drained, err)
		}
	}
	for _, b := range [][]byte{
		nil, {0}, groupFixture(102, 101, 5), groupFixture(101, 999, 5),
		groupFixture(101, 101, 0), groupFixture(101, 101, 6), groupFixture(1, 101, 5),
		append(groupFixture(101, 101, 5), groupFixture(101, 101, 5)...),
		make([]byte, (bbPushMaxGroupMembers+1)*unix.SizeofKinfoProc),
	} {
		if drained, err := bbPushDecodeGroup(b, 101); drained || err != errBBPushGroup {
			t.Fatal("incomplete/invalid snapshot established cleanup")
		}
	}
}

func TestBBPushGroupNativeUnreapedLeader(t *testing.T) {
	for _, exited := range []bool{false, true} {
		t.Run(map[bool]string{false: "live", true: "exited"}[exited], func(t *testing.T) {
			kq, err := unix.Kqueue()
			if err != nil {
				t.Fatal(err)
			}
			defer unix.Close(kq)
			cmd := exec.Command("/bin/sleep", "5")
			if exited {
				cmd = exec.Command("/usr/bin/true")
			}
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			// Cleanup is confined to this unreaped child/group. Wait exactly once.
			defer func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() }()
			if exited {
				change := []unix.Kevent_t{{Ident: uint64(cmd.Process.Pid), Filter: unix.EVFILT_PROC, Flags: unix.EV_ADD | unix.EV_ONESHOT, Fflags: unix.NOTE_EXIT}}
				events := make([]unix.Kevent_t, 1)
				timeout := unix.NsecToTimespec(int64(2 * time.Second))
				n, err := unix.Kevent(kq, change, events, &timeout)
				if err != nil || n != 1 || events[0].Fflags&unix.NOTE_EXIT == 0 {
					t.Fatalf("owned exit observation failed: %v", err)
				}
			}
			drained, err := bbPushGroupDrained(cmd.Process.Pid)
			if err != nil || drained != exited {
				t.Fatalf("native group state: drained=%v error=%v", drained, err)
			}
		})
	}
}
