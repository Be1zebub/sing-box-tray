//go:build windows

package process

import (
	"bufio"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/Be1zebub/sing-box-tray/internal/logbuf"
)

// Manager owns the sing-box child process lifecycle.
type Manager struct {
	singBoxPath string
	buf         *logbuf.Buffer
	onCrash     func()

	mu           sync.Mutex
	cmd          *exec.Cmd
	waitDone     chan struct{}
	expectedStop bool
	job          windows.Handle
}

func NewManager(singBoxPath string, buf *logbuf.Buffer, onCrash func()) *Manager {
	return &Manager{
		singBoxPath: singBoxPath,
		buf:         buf,
		onCrash:     onCrash,
	}
}

func (m *Manager) Start(configPath string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd != nil {
		return fmt.Errorf("process already running")
	}

	cmd := exec.Command(m.singBoxPath, "run", "-c", configPath)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		// CREATE_NO_WINDOW prevents a console window from appearing.
		// CREATE_NEW_PROCESS_GROUP lets us send CTRL_BREAK_EVENT for graceful stop.
		CreationFlags: windows.CREATE_NO_WINDOW | windows.CREATE_NEW_PROCESS_GROUP,
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start sing-box: %w", err)
	}
	if err := m.track(cmd); err != nil {
		m.buf.Append(fmt.Sprintf("[tray] sing-box job: %s", err))
	}

	m.cmd = cmd
	m.waitDone = make(chan struct{})
	m.expectedStop = false

	go drainPipe(stdout, m.buf)
	go drainPipe(stderr, m.buf)
	go m.watch()

	return nil
}

// track puts the child in a job that Windows destroys when this process
// exits, including a kill from Task Manager. OnExit still stops it first.
func (m *Manager) track(cmd *exec.Cmd) error {
	if m.job == 0 {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return err
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
			BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
				LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
			},
		}
		if _, err := windows.SetInformationJobObject(
			job,
			windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)),
			uint32(unsafe.Sizeof(info)),
		); err != nil {
			windows.CloseHandle(job)
			return err
		}
		m.job = job
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(proc)
	return windows.AssignProcessToJobObject(m.job, proc)
}

// Stop sends a terminate signal and waits up to timeout before force-killing.
func (m *Manager) Stop(timeout time.Duration) {
	m.mu.Lock()
	cmd := m.cmd
	if cmd == nil {
		m.mu.Unlock()
		return
	}
	m.expectedStop = true
	waitDone := m.waitDone
	m.mu.Unlock()

	// Attempt graceful termination via Ctrl+Break to the process group,
	// then force-kill after timeout.
	_ = windows.GenerateConsoleCtrlEvent(windows.CTRL_BREAK_EVENT, uint32(cmd.Process.Pid))

	select {
	case <-waitDone:
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		<-waitDone
	}
}

func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cmd != nil
}

func (m *Manager) SetSingBoxPath(path string) {
	m.mu.Lock()
	m.singBoxPath = path
	m.mu.Unlock()
}

func (m *Manager) watch() {
	err := m.cmd.Wait()

	m.mu.Lock()
	expected := m.expectedStop
	m.cmd = nil
	waitDone := m.waitDone
	m.mu.Unlock()

	close(waitDone)

	if !expected {
		if err != nil {
			m.buf.Append(fmt.Sprintf("[tray] sing-box exited: %s", err))
		} else {
			m.buf.Append("[tray] sing-box exited unexpectedly")
		}
		m.onCrash()
	}
}

func drainPipe(r io.Reader, buf *logbuf.Buffer) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		buf.Append(scanner.Text())
	}
}
