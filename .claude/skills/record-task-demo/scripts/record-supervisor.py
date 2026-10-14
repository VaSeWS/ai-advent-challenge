#!/usr/bin/env python3
"""Record only the measured content viewport of the selected Terminal tab."""
from __future__ import annotations

import array
import ctypes
import json
import os
import re
import secrets
import select
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import termios
import time
import tty
from pathlib import Path
from typing import Any

SCRIPT = Path(__file__).resolve()

CREATE_NATIVE_TAB_SCRIPT = '''on run argv
 tell application "Terminal"
  set previousIDs to id of every window
  set previousTTYs to {}
  repeat with w in windows
   repeat with t in tabs of w
    if (count of (processes of t)) > 0 then
     set end of previousTTYs to (tty of t as text)
    end if
   end repeat
  end repeat
  set blankTab to do script ""
  set blankTTY to tty of blankTab as text
  set ownedID to ""
  set matches to 0
  repeat with w in windows
   repeat with t in tabs of w
    if (count of (processes of t)) > 0 then
     if (tty of t as text) is blankTTY then
      set ownedID to id of w as text
      set matches to matches + 1
     end if
    end if
   end repeat
  end repeat
  if matches is not 1 then return "ambiguous|" & blankTTY & "|" & ownedID
  if previousTTYs contains blankTTY then return "preexisting|" & blankTTY & "|" & ownedID
  if previousIDs contains (ownedID as integer) then return "preexisting-window|" & blankTTY & "|" & ownedID
  return "owned|" & blankTTY & "|" & ownedID
 end tell
end run'''
LAUNCH_NATIVE_COMMAND_SCRIPT = '''on run argv
 set taskCommand to item 1 of argv
 set expectedID to item 2 of argv
 set expectedTTY to item 3 of argv
 set shellNames to {"bash", "-bash", "zsh", "-zsh", "sh", "-sh", "fish", "-fish", "ksh", "-ksh", "tcsh", "-tcsh", "csh", "-csh", "dash", "-dash"}
 set wrapperNames to {"login", "-login"}
 set deadline to (current date) + 5
 tell application "Terminal"
  set targetWindow to missing value
  set windowMatches to 0
  repeat with w in windows
   if (id of w as text) is expectedID then
    set targetWindow to w
    set windowMatches to windowMatches + 1
   end if
  end repeat
  if windowMatches is not 1 then error "owned Terminal window identity is missing or ambiguous"
  repeat
   set targetTab to missing value
   set tabMatches to 0
   repeat with t in tabs of targetWindow
    if tty of t is expectedTTY then
     set targetTab to t
     set tabMatches to tabMatches + 1
    end if
   end repeat
   if tabMatches is not 1 then error "owned Terminal TTY is missing or ambiguous"
   if tty of selected tab of targetWindow is not expectedTTY then error "owned Terminal tab selection changed"
   if not (frontmost of targetWindow) then error "owned Terminal window is no longer frontmost"
   set runningProcesses to processes of targetTab
   set shellReady to false
   set safeProcesses to true
   repeat with p in runningProcesses
    set processName to p as text
    if shellNames contains processName then
     set shellReady to true
    else if wrapperNames does not contain processName then
     set safeProcesses to false
    end if
   end repeat
   if not safeProcesses then error "owned Terminal tab is running a non-shell foreground process"
   if shellReady and not (busy of targetTab) then exit repeat
   if (current date) > deadline then error "owned Terminal local shell did not become safely idle"
   delay 0.1
  end repeat
  set taskTab to do script taskCommand in targetTab
  set taskTTY to tty of taskTab
  if taskTTY is not expectedTTY then error "Terminal launched the supervisor in a different tab"
  return taskTTY & "|" & (id of targetWindow as text)
 end tell
end run'''
JOIN_SECONDS = 5.0
DARWIN_SOL_LOCAL = 0
DARWIN_LOCAL_PEERPID = 0x002
SCREEN_LOCK = SCRIPT.parents[4] / ".omp/task-execute/runs/.screen-recording.lock"
EXEC_LAUNCHER = (
    "import os, signal, sys\n"
    "signal.pthread_sigmask(signal.SIG_SETMASK, "
    "{signal.Signals(int(value)) for value in sys.argv[1].split(',') if value})\n"
    "command = sys.argv[2:]\n"
    "os.execvpe(command[0], command, os.environ)\n"
)
TERMINAL_STATE = '''on run argv
 set targetTTY to item 1 of argv
 tell application "Terminal"
  set foundWindow to missing value
  set targetTab to missing value
  set matches to 0
  repeat with w in windows
   repeat with t in tabs of w
    if (count of (processes of t)) > 0 then
     if (tty of t as text) is targetTTY then
      set foundWindow to w
      set targetTab to t
      set matches to matches + 1
     end if
    end if
   end repeat
  end repeat
  if matches is not 1 then error "Terminal TTY is not uniquely identifiable"
  set b to bounds of foundWindow
  set fontSize to font size of targetTab
  set rows to number of rows of targetTab
  set columns to number of columns of targetTab
  set selectedTTY to tty of selected tab of foundWindow
  set isFrontmost to frontmost of foundWindow
  return (id of foundWindow as text) & "|" & (count of tabs of foundWindow) & "|" & ¬
   (item 1 of b as text) & "|" & (item 2 of b as text) & "|" & ¬
   (item 3 of b as text) & "|" & (item 4 of b as text) & "|" & ¬
   (fontSize as text) & "|" & (rows as text) & "|" & (columns as text) & "|" & ¬
   selectedTTY & "|" & (isFrontmost as text)
 end tell
end run'''
SELECT_TAB = '''on run argv
 set targetTTY to item 1 of argv
 set expectedID to item 2 of argv
 set profileName to item 3 of argv
 tell application "Terminal"
  set foundWindow to missing value
  set targetTab to missing value
  set matches to 0
  repeat with w in windows
   repeat with t in tabs of w
    if (count of (processes of t)) > 0 then
     if (tty of t as text) is targetTTY then
      set foundWindow to w
      set targetTab to t
      set matches to matches + 1
     end if
    end if
   end repeat
  end repeat
  if matches is not 1 then error "Terminal TTY is not unique"
  if (id of foundWindow as text) is not expectedID then error "Terminal window identity changed"
  if profileName is not "" then set current settings of targetTab to settings set profileName
  set selected tab of foundWindow to targetTab
  set frontmost of foundWindow to true
 end tell
end run'''


class RecordingError(RuntimeError):
    """A capture prerequisite or retained direct-child lifecycle failed."""


def process_start_identity(pid: int) -> str:
    """Read a process start marker while the process is live."""
    result = subprocess.run(
        ["/bin/ps", "-p", str(pid), "-o", "lstart="],
        check=True, text=True, capture_output=True, timeout=5, env=public_environment(),
    )
    identity = result.stdout.strip()
    if result.returncode != 0 or not identity:
        raise RecordingError(f"could not establish process start identity for PID {pid}")
    return identity


def send_packet(channel: socket.socket, payload: dict[str, Any], fd: int | None = None) -> None:
    """Send one bounded JSON message, optionally transferring the lease descriptor."""
    encoded = json.dumps(payload, sort_keys=True).encode("utf-8")
    if len(encoded) > 1_048_576:
        raise RecordingError("native handoff message exceeds its size limit")
    packet = len(encoded).to_bytes(4, "big") + encoded
    ancillary = []
    if fd is not None:
        ancillary = [(socket.SOL_SOCKET, socket.SCM_RIGHTS, array.array("i", [fd]))]
    sent = channel.sendmsg([packet], ancillary)
    if sent <= 0:
        raise RecordingError("native handoff message was not sent")
    if sent < len(packet):
        channel.sendall(packet[sent:])


def native_peer_pid(channel: socket.socket) -> int:
    """Read Darwin's authenticated AF_UNIX peer PID; fail closed elsewhere."""
    try:
        raw = channel.getsockopt(DARWIN_SOL_LOCAL, DARWIN_LOCAL_PEERPID, 4)
    except OSError as exc:
        raise RecordingError("native handoff cannot authenticate the local socket peer PID") from exc
    if len(raw) != 4:
        raise RecordingError("native handoff returned an invalid local socket peer PID")
    return int.from_bytes(raw, byteorder=sys.byteorder, signed=True)

def receive_packet(channel: socket.socket, *, receive_fd: bool = False) -> tuple[dict[str, Any], int | None]:
    """Receive one bounded JSON message and optional single descriptor."""
    prefix = bytearray()
    ancillary: list[tuple[int, int, bytes]] = []
    while len(prefix) < 4:
        chunk, received, _flags, _address = channel.recvmsg(
            4 - len(prefix), socket.CMSG_SPACE(array.array("i").itemsize)
            if receive_fd and not ancillary else 0,
        )
        if not chunk:
            raise RecordingError("native handoff channel closed before a complete message")
        prefix.extend(chunk)
        ancillary.extend(received)
    size = int.from_bytes(prefix, "big")
    if not 1 <= size <= 1_048_576:
        raise RecordingError("native handoff message has an invalid size")
    payload = bytearray()
    while len(payload) < size:
        chunk = channel.recv(size - len(payload))
        if not chunk:
            raise RecordingError("native handoff channel closed during a message")
        payload.extend(chunk)
    try:
        message = json.loads(payload)
    except (UnicodeDecodeError, json.JSONDecodeError) as exc:
        raise RecordingError("native handoff message is not valid JSON") from exc
    if not isinstance(message, dict):
        raise RecordingError("native handoff message must be an object")
    descriptors: list[int] = []
    for level, kind, data in ancillary:
        if level == socket.SOL_SOCKET and kind == socket.SCM_RIGHTS:
            values = array.array("i")
            values.frombytes(data[:len(data) - len(data) % values.itemsize])
            descriptors.extend(values)
    if receive_fd and len(descriptors) != 1:
        for descriptor in descriptors:
            os.close(descriptor)
        raise RecordingError("native handoff did not transfer exactly one lease descriptor")
    if not receive_fd and descriptors:
        for descriptor in descriptors:
            os.close(descriptor)
        raise RecordingError("unexpected descriptor on native handoff channel")
    if receive_fd:
        os.set_inheritable(descriptors[0], False)
    return message, descriptors[0] if receive_fd else None


class ScreenLease:
    """One project-wide capture owner; uncertain cleanup requires reconciliation."""

    def __init__(self, demo: Path) -> None:
        self.fd: int | None = None
        self.identity: tuple[int, int] | None = None
        self.children: dict[str, subprocess.Popen[bytes]] = {}
        self.stop_confirmed = True
        self.metadata: dict[str, object] = {
            "token": secrets.token_hex(32),
            "pid": os.getpid(),
            "host": socket.gethostname(),
            "demo": str(demo),
            "task": str(demo.parent.parent),
            "acquired_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
            "lifecycle": {},
            "parent_start": process_start_identity(os.getpid()),
        }

    def __enter__(self) -> ScreenLease:
        previous = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            try:
                SCREEN_LOCK.parent.mkdir(parents=True, exist_ok=True)
                try:
                    self.fd = os.open(SCREEN_LOCK, os.O_CREAT | os.O_EXCL | os.O_RDWR, 0o600)
                except FileExistsError as exc:
                    raise RecordingError(
                        f"screen recording is busy or unreconciled: {SCREEN_LOCK}; "
                        "wait for the owner to confirm release; never steal the lease"
                    ) from exc
                entry = os.fstat(self.fd)
                self.identity = (entry.st_dev, entry.st_ino)
                self.update()
            finally:
                signal.pthread_sigmask(signal.SIG_SETMASK, previous)
        except BaseException:
            if self.fd is not None:
                self.__exit__(None, None, None)
            raise
        return self

    def adopt(self, fd: int, metadata: dict[str, object], identity: tuple[int, int]) -> None:
        """Become the sole lease owner after an authenticated native-tab handoff."""
        try:
            entry = os.fstat(fd)
            current = os.lstat(SCREEN_LOCK)
            with os.fdopen(os.open(SCREEN_LOCK, os.O_RDONLY | os.O_NOFOLLOW), "r", encoding="utf-8") as reader:
                recorded = json.load(reader)
            if (entry.st_dev, entry.st_ino) != identity or (current.st_dev, current.st_ino) != identity:
                raise RecordingError("native handoff lease identity changed")
            if recorded.get("token") != metadata.get("token") or recorded.get("pid") != metadata.get("pid"):
                raise RecordingError("native handoff lease token or parent owner changed")
            self.fd = fd
            self.identity = identity
            self.metadata = metadata
            self.metadata["parent_pid"] = metadata["pid"]
            self.metadata["parent_start"] = metadata["parent_start"]
            self.metadata["pid"] = os.getpid()
            self.metadata["child_start"] = process_start_identity(os.getpid())
            self.update(native_tty=os.ttyname(sys.stdin.fileno()), native_handoff_state="child-owned")
        except BaseException:
            if self.fd == fd:
                self.fd = None
            try:
                os.close(fd)
            except OSError:
                pass
            raise

    def relinquish(self) -> None:
        """Close the parent's duplicate after the native child confirms adoption."""
        if self.fd is not None:
            os.close(self.fd)
            self.fd = None

    def update(self, **references: object) -> None:
        """Update the retained inode, never replace the exclusive lock file."""
        if self.fd is None:
            raise RecordingError("screen recording lease is not held")
        lifecycle = self.metadata["lifecycle"]
        assert isinstance(lifecycle, dict)
        lifecycle.update(references)
        payload = (json.dumps(self.metadata, sort_keys=True) + "\n").encode("utf-8")
        os.lseek(self.fd, 0, os.SEEK_SET)
        remaining = memoryview(payload)
        while remaining:
            written = os.write(self.fd, remaining)
            if written == 0:
                raise RecordingError("screen recording lease metadata write failed")
            remaining = remaining[written:]
        os.ftruncate(self.fd, len(payload))
        os.fsync(self.fd)

    def spawn(self, name: str, command: list[str], **kwargs: object) -> subprocess.Popen[bytes]:
        """Restore the caller's signal mask in the child and retain its exec handle."""
        previous = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
        try:
            mask = ",".join(str(int(item)) for item in previous)
            child = subprocess.Popen(
                [sys.executable, "-c", EXEC_LAUNCHER, mask, *command], **kwargs
            )
            self.children[name] = child
            self.stop_confirmed = False
            self.update(**{f"{name}_pid": child.pid})
            return child
        finally:
            signal.pthread_sigmask(signal.SIG_SETMASK, previous)

    def __exit__(self, _kind: object, _error: object, _traceback: object) -> None:
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        try:
            if not self.stop_confirmed or any(child.poll() is None for child in self.children.values()):
                print(f"screen recording lease retained for explicit reconciliation: {SCREEN_LOCK}", file=sys.stderr)
                return
            if self.fd is None:
                return
            current = os.lstat(SCREEN_LOCK)
            if (current.st_dev, current.st_ino) != self.identity:
                raise RecordingError("screen recording lease identity changed; replacement was not removed")
            with os.fdopen(os.open(SCREEN_LOCK, os.O_RDONLY | os.O_NOFOLLOW), "r", encoding="utf-8") as reader:
                entry = os.fstat(reader.fileno())
                if (entry.st_dev, entry.st_ino) != self.identity:
                    raise RecordingError("screen recording lease identity changed; replacement was not removed")
                if json.load(reader).get("token") != self.metadata["token"]:
                    raise RecordingError("screen recording lease token changed; replacement was not removed")
            current = os.lstat(SCREEN_LOCK)
            if (current.st_dev, current.st_ino) != self.identity:
                raise RecordingError("screen recording lease identity changed; replacement was not removed")
            os.unlink(SCREEN_LOCK)
        except (OSError, ValueError, AttributeError, RecordingError) as exc:
            print(f"error: lease release unconfirmed: {exc}; reconcile {SCREEN_LOCK}", file=sys.stderr)
        finally:
            if self.fd is not None:
                os.close(self.fd)
                self.fd = None


class ShutdownRequested(Exception):
    """A termination signal requesting bounded cleanup of retained children."""

    def __init__(self, signum: int, reason: str | None = None) -> None:
        self.signum = signum
        self.reason = reason or f"interrupted by signal {signum}"


def interrupt_supervisor(signum: int, _frame: object) -> None:
    """Route termination through the direct-child cleanup path."""
    signal.signal(signal.SIGINT, signal.SIG_IGN)
    signal.signal(signal.SIGTERM, signal.SIG_IGN)
    raise ShutdownRequested(signum, f"interrupted by signal {signum}")

def check_native_parent(channel: socket.socket | None) -> None:
    """Turn authenticated cancellation or channel loss into normal capture cleanup."""
    if channel is None:
        return
    try:
        readable, _, _ = select.select([channel], [], [], 0)
    except OSError as exc:
        raise ShutdownRequested(signal.SIGTERM, "native parent channel failed; cancelling capture") from exc
    if not readable:
        return
    try:
        message, _ = receive_packet(channel)
    except (OSError, RecordingError) as exc:
        raise ShutdownRequested(signal.SIGTERM, "native parent channel was lost; cancelling capture") from exc
    signum = message.get("signum")
    if (
        message.get("cancel") is True
        and type(signum) is int
        and signum in {signal.SIGINT, signal.SIGTERM}
    ):
        raise ShutdownRequested(signum, "recording cancellation requested by its caller")
    raise ShutdownRequested(signal.SIGTERM, "native parent channel sent an invalid message; cancelling capture")

def create_tracked_tempdir(
    lease: ScreenLease, owned_paths: dict[str, Path], key: str,
    prefix: str, directory: Path | None = None,
) -> Path:
    """Allocate a private directory and persist its reconciliation path atomically."""
    previous = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
    try:
        path = Path(tempfile.mkdtemp(prefix=prefix, dir=directory))
        owned_paths[key] = path
        lease.update(**{key: str(path)})
        return path
    finally:
        signal.pthread_sigmask(signal.SIG_SETMASK, previous)


def create_tracked_text_file(
    lease: ScreenLease, owned_paths: dict[str, Path], key: str, path: Path, text: str,
) -> None:
    """Record a private path before creating its file, with signals held across both steps."""
    previous = signal.pthread_sigmask(signal.SIG_BLOCK, {signal.SIGINT, signal.SIGTERM})
    try:
        owned_paths[key] = path
        lease.update(**{key: str(path)})
        path.write_text(text, encoding="ascii")
    finally:
        signal.pthread_sigmask(signal.SIG_SETMASK, previous)


def permission_preflight() -> int:
    """Check this process's Screen Recording authorization without capture."""
    try:
        core_graphics = ctypes.CDLL(
            "/System/Library/Frameworks/CoreGraphics.framework/CoreGraphics"
        )
        preflight = core_graphics.CGPreflightScreenCaptureAccess
    except (OSError, AttributeError) as exc:
        print(f"error: ScreenCaptureKit permission preflight is unavailable: {exc}", file=sys.stderr)
        return 2
    preflight.restype = ctypes.c_bool
    if not preflight():
        print("error: Screen Recording permission is not granted to this terminal app", file=sys.stderr)
        return 1
    print("Screen Recording permission is available")
    return 0




def public_environment() -> dict[str, str]:
    """Remove task credentials from recorder, compiler, and UI helper processes."""
    env = os.environ.copy()
    for name in (
        "GROQ_API_KEY", "DEEPSEEK_API_KEY", "YANDEX_DISK_TOKEN",
        "DAY30_SERVICE_TOKEN", "TELEGRAM_BOT_TOKEN", "TELEGRAM_CHAT_ID",
    ):
        env.pop(name, None)
    return env


def positive_env(name: str, default: int, maximum: int = 86400) -> int:
    """Read one bounded positive integer setting."""
    value = os.environ.get(name, str(default))
    try:
        parsed = int(value)
    except ValueError as exc:
        raise RecordingError(f"{name} must be an integer from 1 to {maximum}") from exc
    if parsed < 1 or parsed > maximum:
        raise RecordingError(f"{name} must be an integer from 1 to {maximum}")
    return parsed


def apple(script: str, *args: str) -> str:
    """Run a fixed Terminal query/configuration script with bounded arguments."""
    try:
        result = subprocess.run(
            ["osascript", "-", *args], input=script, text=True, capture_output=True,
            timeout=10, check=False, env=public_environment(),
        )
    except subprocess.TimeoutExpired as exc:
        raise RecordingError("Terminal identity/configuration query exceeded its 10-second bound") from exc
    if result.returncode:
        detail = result.stderr.strip() or "AppleScript failed"
        raise RecordingError(detail)
    return result.stdout.strip()


def terminal_state(tty_name: str) -> tuple[str, int, int, int, int, int, int, int, int, str, bool]:
    """Read the owned tab's window, selected state, geometry, and actual font."""
    try:
        parts = apple(TERMINAL_STATE, tty_name).split("|")
        if len(parts) != 11 or parts[10] not in {"true", "false"}:
            raise ValueError
        return (
            parts[0], *(int(value) for value in parts[1:9]), parts[9], parts[10] == "true",
        )
    except ValueError as exc:
        raise RecordingError("Terminal returned invalid identity, selected-tab, or geometry data") from exc


def check_window(
    tty_name: str,
    expected: tuple[str, int, int, int, int, int, int, int, int, str, bool],
) -> None:
    """Fail closed if the owned selected tab, font, or window geometry changed."""
    actual = terminal_state(tty_name)
    if actual != expected or actual[6] < 16 or actual[7] < 1 or actual[8] < 1:
        raise RecordingError("Terminal selected tab, frontmost window, font, or geometry changed")


def query_tty_geometry(tty_name: str) -> tuple[int, int, int, int, int, int]:
    """Measure Terminal's selected text viewport using bounded window-operation replies."""
    fd = os.open(tty_name, os.O_RDWR | os.O_NOCTTY)
    original = termios.tcgetattr(fd)
    data = bytearray()
    try:
        tty.setraw(fd, termios.TCSANOW)
        request = memoryview(b"\x1b[13;2t\x1b[14t\x1b[18t")
        while request:
            written = os.write(fd, request)
            if written <= 0:
                raise RecordingError("Terminal viewport query could not be written")
            request = request[written:]
        deadline = time.monotonic() + 2.0
        while time.monotonic() < deadline and len(data) < 4096:
            readable, _, _ = select.select([fd], [], [], max(0.0, deadline - time.monotonic()))
            if not readable:
                break
            chunk = os.read(fd, min(1024, 4096 - len(data)))
            if not chunk:
                break
            data.extend(chunk)
            replies = re.findall(rb"\x1b\[(\d+);(\d+);(\d+)t", data)
            if {int(reply[0]) for reply in replies} == {3, 4, 8} and len(replies) == 3:
                break
    finally:
        try:
            termios.tcsetattr(fd, termios.TCSANOW, original)
        finally:
            os.close(fd)
    matches = list(re.finditer(rb"\x1b\[(\d+);(\d+);(\d+)t", data))
    remainder = re.sub(rb"\x1b\[\d+;\d+;\d+t", b"", data)
    if remainder or len(matches) != 3:
        raise RecordingError("Terminal did not return exactly the three requested viewport measurements")
    reports = {int(match.group(1)): tuple(int(part) for part in match.groups()[1:]) for match in matches}
    if set(reports) != {3, 4, 8}:
        raise RecordingError("Terminal returned incomplete or duplicate viewport measurements")
    x, y = reports[3]
    height, width = reports[4]
    rows, columns = reports[8]
    if width < 1 or height < 1 or rows < 1 or columns < 1:
        raise RecordingError("Terminal reported empty viewport geometry")
    return x, y, width, height, rows, columns


def wait_child(child: subprocess.Popen[bytes], timeout: float) -> int | None:
    """Wait for a retained direct child for at most timeout seconds."""
    try:
        return child.wait(timeout=max(0.0, timeout))
    except subprocess.TimeoutExpired:
        return None


def stop_recorder(recorder: subprocess.Popen[bytes]) -> int | None:
    """Request SCK finalization and retain the handle until the recorder exits."""
    if recorder.poll() is None:
        try:
            recorder.send_signal(signal.SIGINT)
        except ProcessLookupError:
            pass
    status = wait_child(recorder, JOIN_SECONDS)
    if status is None:
        raise RecordingError("ScreenCaptureKit finalization is unconfirmed; output was not published")
    return status


def stop_demo(demo: subprocess.Popen[bytes]) -> bool:
    """Stop only the retained demo child; never infer ownership from process listings."""
    if demo.poll() is not None:
        return True
    try:
        demo.send_signal(signal.SIGINT)
    except ProcessLookupError:
        return wait_child(demo, JOIN_SECONDS) is not None
    if wait_child(demo, JOIN_SECONDS) is not None:
        return True
    try:
        demo.send_signal(signal.SIGTERM)
    except ProcessLookupError:
        pass
    return wait_child(demo, JOIN_SECONDS) is not None


def check_private_workload(path: Path) -> None:
    """Reject a demo whose private tmux lifecycle was not explicitly stopped."""
    try:
        state = path.read_text(encoding="ascii").strip()
    except OSError as exc:
        raise RecordingError("private demo lifecycle state is unavailable") from exc
    if state not in {"idle", "stopped"}:
        raise RecordingError("private demo tmux shutdown is unconfirmed; recording is not accepted")


def native_handoff(demo: Path, output: Path) -> int:
    """Create a verified owned Terminal tab and transfer the already-held lease."""
    lease = ScreenLease(demo)
    socket_dir = Path(tempfile.mkdtemp(prefix=".rec-", dir="/tmp"))
    socket_path = socket_dir / "s"
    server: socket.socket | None = None
    lease_acquired = False
    try:
        server = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        server.bind(str(socket_path))
        os.chmod(socket_path, 0o600)
        server.listen(1)
        lease.__enter__()
        lease_acquired = True
        lease.stop_confirmed = False
        lease.update(
            native_handoff_state="pending", native_socket=str(socket_path),
            native_socket_dir=str(socket_dir), output=str(output),
        )
        opened = apple(CREATE_NATIVE_TAB_SCRIPT).split("|")
        if len(opened) != 3:
            raise RecordingError("Terminal did not return the blank native tab's ownership identity")
        ownership, blank_tty, terminal_id = opened
        if terminal_id:
            lease.update(
                native_window_id=terminal_id,
                native_window_owned=ownership == "owned",
                native_tab_tty=blank_tty,
            )
        if ownership != "owned" or not blank_tty or not terminal_id:
            raise RecordingError("Terminal did not create a uniquely identified new owned window and tab; command not launched")
        command = shlex.join([
            sys.executable, str(SCRIPT), "--native-child", str(socket_path), str(demo), str(output),
        ])
        launched = apple(
            LAUNCH_NATIVE_COMMAND_SCRIPT, "exec " + command, terminal_id, blank_tty,
        ).split("|")
        if len(launched) != 2 or launched[0] != blank_tty or launched[1] != terminal_id:
            raise RecordingError("supervisor did not launch in the verified idle owned TTY")
        expected_tty = blank_tty
        lease.update(native_tty=expected_tty, terminal_id=terminal_id)
        server.settimeout(120)
        channel, _address = server.accept()
        with channel:
            channel.settimeout(120)
            peer_pid = native_peer_pid(channel)
            request, _ = receive_packet(channel)
            child_pid = request.get("pid")
            child_start = request.get("start")
            if (
                request.get("tty") != expected_tty
                or request.get("demo") != str(demo)
                or request.get("output") != str(output)
                or peer_pid != child_pid
                or not isinstance(child_pid, int)
                or child_pid <= 1
                or not isinstance(child_start, str)
                or process_start_identity(child_pid) != child_start
            ):
                raise RecordingError("native tab startup identity or requested workload did not match")
            try:
                server.close()
                server = None
                os.unlink(socket_path)
                os.rmdir(socket_dir)
            except OSError as exc:
                lease.update(
                    native_rendezvous_cleanup_failed=True,
                    native_rendezvous_cleanup_error=str(exc),
                    native_socket=str(socket_path),
                    native_socket_dir=str(socket_dir),
                )
                raise RecordingError(
                    f"native rendezvous cleanup failed at {socket_path} or {socket_dir}: {exc}"
                ) from exc
            lease.update(native_rendezvous_cleanup_failed=False, native_rendezvous_removed=True)
            parent_start = str(lease.metadata["parent_start"])
            lease.update(
                parent_start=parent_start,
                child_pid=child_pid,
                child_start=child_start,
                native_handoff_state="transferring",
            )
            identity = lease.identity
            if identity is None or lease.fd is None:
                raise RecordingError("native handoff has no active screen lease")
            ticket = {
                "demo": str(demo), "output": str(output), "parent_pid": os.getpid(),
                "parent_start": parent_start, "child_pid": child_pid, "child_start": child_start,
                "host": socket.gethostname(), "token": lease.metadata["token"],
                "device": identity[0], "inode": identity[1], "metadata": lease.metadata,
                "tty": expected_tty, "terminal_id": terminal_id,
                "environment": dict(os.environ),
            }
            send_packet(channel, ticket, lease.fd)
            response, _ = receive_packet(channel)
            if response != {"adopted": True}:
                raise RecordingError("native child did not confirm lease adoption; ownership retained")
            lease.relinquish()
            send_packet(channel, {"parent_relinquished": True})
            channel.settimeout(
                positive_env("TIMEOUT", 7200)
                + positive_env("CAPTURE_READY_TIMEOUT", 120, 3600)
                + JOIN_SECONDS * 3 + 30
            )
            try:
                response, _ = receive_packet(channel)
            except ShutdownRequested as interrupted:
                try:
                    send_packet(channel, {"cancel": True, "signum": interrupted.signum})
                    channel.settimeout(JOIN_SECONDS * 4 + 30)
                    response, _ = receive_packet(channel)
                except (OSError, RecordingError, subprocess.SubprocessError) as exc:
                    print(
                        f"error: {interrupted.reason}; child shutdown acknowledgment is unconfirmed: {exc}; "
                        f"reconcile {SCREEN_LOCK}",
                        file=sys.stderr,
                    )
                    return 1
                status = response.get("status")
                if type(status) is not int or type(response.get("released")) is not bool:
                    raise RecordingError("child cancellation response is malformed; reconcile the retained lease")
                if not response["released"]:
                    raise RecordingError(f"child cancellation cleanup is unconfirmed; reconcile {SCREEN_LOCK}")
                print(f"error: {interrupted.reason}; child cleanup and lease release confirmed", file=sys.stderr)
                return 128 + interrupted.signum
            status = response.get("status")
            if type(status) is not int or type(response.get("released")) is not bool:
                raise RecordingError("native child final status is missing or malformed; reconcile the retained lease")
            if not response["released"]:
                raise RecordingError(f"native child ended without confirmed lease release; reconcile {SCREEN_LOCK}")
            return status
    except (OSError, subprocess.SubprocessError, ValueError, RecordingError, KeyError, TypeError) as exc:
        print(f"error: native Terminal handoff failed: {exc}; reconcile {SCREEN_LOCK}", file=sys.stderr)
        return 1
    finally:
        if server is not None:
            server.close()
        if not lease_acquired:
            try:
                if os.path.lexists(socket_path):
                    os.unlink(socket_path)
                os.rmdir(socket_dir)
            except OSError as exc:
                print(
                    f"error: unowned rendezvous cleanup failed at {socket_path} or {socket_dir}: {exc}",
                    file=sys.stderr,
                )
        elif lease.fd is not None:
            # A failed or ambiguous handoff must leave the same lock inode for reconciliation.
            lease.relinquish()


def native_child(socket_path: str, demo_arg: str, output_arg: str) -> int:
    """Authenticate, adopt, and run the supervisor in its owned Terminal window."""
    demo = Path(demo_arg).expanduser().resolve()
    output = Path(output_arg).expanduser().resolve()
    channel = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    lease: ScreenLease | None = None
    try:
        if not sys.stdin.isatty() or not sys.stdout.isatty():
            raise RecordingError("native child did not receive Terminal-tab stdin and stdout")
        tty_name = os.ttyname(sys.stdin.fileno())
        start = process_start_identity(os.getpid())
        channel.connect(socket_path)
        channel.settimeout(120)
        peer_pid = native_peer_pid(channel)
        send_packet(channel, {
            "pid": os.getpid(), "start": start, "tty": tty_name,
            "demo": str(demo), "output": str(output),
        })
        ticket, received_fd = receive_packet(channel, receive_fd=True)
        if received_fd is None:
            raise RecordingError("native handoff omitted the lease descriptor")
        metadata = ticket.get("metadata")
        lifecycle = metadata.get("lifecycle") if isinstance(metadata, dict) else None
        identity = (ticket.get("device"), ticket.get("inode"))
        if (
            not isinstance(metadata, dict)
            or not isinstance(lifecycle, dict)
            or ticket.get("demo") != str(demo)
            or metadata.get("demo") != str(demo)
            or ticket.get("output") != str(output)
            or lifecycle.get("output") != str(output)
            or ticket.get("child_pid") != os.getpid()
            or ticket.get("child_start") != start
            or ticket.get("parent_pid") != peer_pid
            or not isinstance(ticket.get("parent_pid"), int)
            or ticket.get("parent_pid") <= 1
            or ticket.get("parent_pid") != metadata.get("pid")
            or ticket.get("parent_start") != metadata.get("parent_start")
            or ticket.get("host") != socket.gethostname()
            or ticket.get("host") != metadata.get("host")
            or ticket.get("token") != metadata.get("token")
            or ticket.get("tty") != tty_name
            or lifecycle.get("native_tab_tty") != tty_name
            or ticket.get("terminal_id") != lifecycle.get("native_window_id")
            or not all(isinstance(value, int) for value in identity)
            or process_start_identity(ticket.get("parent_pid")) != ticket.get("parent_start")
        ):
            os.close(received_fd)
            raise RecordingError("native handoff ticket failed its process, workload, host, or lease binding")
        environment = ticket.get("environment")
        if not isinstance(environment, dict) or not all(
            isinstance(key, str) and isinstance(value, str)
            for key, value in environment.items()
        ):
            os.close(received_fd)
            raise RecordingError("native handoff omitted the original caller environment")
        os.environ.clear()
        os.environ.update(environment)
        lease = ScreenLease(demo)
        lease.stop_confirmed = False
        lease.adopt(received_fd, metadata, identity)
        send_packet(channel, {"adopted": True})
        response, _ = receive_packet(channel)
        if response != {"parent_relinquished": True}:
            raise RecordingError("parent did not confirm lease relinquishment; capture not started")
        try:
            check_native_parent(channel)
            if not demo.is_file():
                raise RecordingError(f"no such demo script: {demo}")
            repo_result = subprocess.run(
                ["git", "-C", str(demo.parent), "rev-parse", "--show-toplevel"],
                check=True, text=True, capture_output=True, timeout=5, env=public_environment(),
            )
            repo = Path(repo_result.stdout.strip())
            relative_demo = demo.relative_to(repo)
            match_week = re.match(r"week-0*([0-9]+)/", relative_demo.as_posix())
            match_day = re.search(r"/day-([0-9]+)/", "/" + relative_demo.as_posix())
            week = match_week.group(1) if match_week else ""
            day = match_day.group(1) if match_day else ""
            timeout = positive_env("TIMEOUT", 7200)
            ready_timeout = positive_env("CAPTURE_READY_TIMEOUT", 120, 3600)
            check_native_parent(channel)
            permission_status = permission_preflight()
            if permission_status != 0:
                raise RecordingError("Screen Recording permission preflight failed in the owned Terminal tab")
            terminal_id = ticket.get("terminal_id")
            if not isinstance(terminal_id, str) or not terminal_id:
                raise RecordingError("native handoff ticket omitted its Terminal window identity")
            status = capture(
                lease, repo, demo, output, week, day, tty_name, timeout, ready_timeout,
                terminal_id, channel,
            )
        except ShutdownRequested as exc:
            lifecycle = lease.metadata.get("lifecycle")
            if (
                not lease.children
                and isinstance(lifecycle, dict)
                and not any(name in lifecycle for name in ("work", "capture_dir", "recording"))
            ):
                lease.stop_confirmed = True
            status = 128 + exc.signum
        except KeyboardInterrupt:
            lifecycle = lease.metadata.get("lifecycle")
            if (
                not lease.children
                and isinstance(lifecycle, dict)
                and not any(name in lifecycle for name in ("work", "capture_dir", "recording"))
            ):
                lease.stop_confirmed = True
            status = 130
        lease.__exit__(None, None, None)
        released = not os.path.lexists(SCREEN_LOCK)
        send_packet(channel, {"status": status, "released": released})
        return status
    except (OSError, subprocess.SubprocessError, ValueError, RecordingError, KeyError, TypeError) as exc:
        if lease is not None and lease.fd is not None:
            lease.stop_confirmed = False
            lease.__exit__(None, None, None)
        released = not os.path.lexists(SCREEN_LOCK)
        if released:
            print(f"error: native Terminal child failed: {exc}; lease release confirmed", file=sys.stderr)
        else:
            print(
                f"error: native Terminal child failed: {exc}; lease retained for reconciliation at {SCREEN_LOCK}",
                file=sys.stderr,
            )
        try:
            send_packet(channel, {"status": 1, "released": released})
        except OSError:
            pass
        return 1
    finally:
        channel.close()


def run(demo_arg: str, output_arg: str | None) -> int:
    """Capture the selected Terminal tab's measured text viewport."""
    demo = Path(demo_arg).expanduser().resolve()
    if not demo.is_file():
        raise RecordingError(f"no such demo script: {demo_arg}")
    try:
        repo = Path(subprocess.run(
            ["git", "-C", str(demo.parent), "rev-parse", "--show-toplevel"],
            check=True, text=True, capture_output=True, timeout=5, env=public_environment(),
        ).stdout.strip())
        relative_demo = demo.relative_to(repo)
    except (OSError, subprocess.SubprocessError, ValueError) as exc:
        raise RecordingError("demo must be inside a git checkout") from exc

    match_week = re.match(r"week-0*([0-9]+)/", relative_demo.as_posix())
    match_day = re.search(r"/day-([0-9]+)/", "/" + relative_demo.as_posix())
    week = match_week.group(1) if match_week else ""
    day = match_day.group(1) if match_day else ""
    default_name = f"day-{day}-demo.mp4" if day else demo.with_suffix(".mp4").name
    requested = Path(output_arg).expanduser() if output_arg else demo.parent / default_name
    if os.path.lexists(requested):
        raise RecordingError(f"refusing to overwrite existing output: {requested}")
    output = requested.resolve()
    if output == demo or os.path.lexists(output):
        raise RecordingError(f"refusing to overwrite existing output: {requested}")
    if os.environ.get("CAPTURE_WINDOW", "1") != "1":
        raise RecordingError("CAPTURE_WINDOW=0 rectangle capture is unsupported; ScreenCaptureKit is required")
    if any(name in os.environ for name in ("W", "H", "X", "Y")):
        raise RecordingError("W/H/X/Y layout overrides are unsupported; shared Terminal geometry is never changed")
    timeout = positive_env("TIMEOUT", 7200)
    ready_timeout = positive_env("CAPTURE_READY_TIMEOUT", 120, 3600)
    if not sys.stdin.isatty() or not sys.stdout.isatty():
        return native_handoff(demo, output)
    permission_status = permission_preflight()
    if permission_status == 1:
        raise RecordingError("Screen Recording permission is not granted; no recording was started")
    if permission_status != 0:
        raise RecordingError("Screen Recording permission preflight failed; no recording was started")
    tty_name = os.ttyname(sys.stdin.fileno())
    with ScreenLease(demo) as lease:
        return capture(lease, repo, demo, output, week, day, tty_name, timeout, ready_timeout)

def capture(
    lease: ScreenLease, repo: Path, demo: Path, output: Path, week: str, day: str,
    tty_name: str, timeout: int, ready_timeout: int,
    expected_terminal_id: str | None = None, parent_channel: socket.socket | None = None,
) -> int:
    """Run all Terminal operations and capture cleanup while holding the lease."""
    check_native_parent(parent_channel)
    initial = terminal_state(tty_name)
    terminal_id = initial[0]
    if expected_terminal_id is not None and terminal_id != expected_terminal_id:
        raise RecordingError("native Terminal tab moved to a different window before capture")
    lease.update(tty=tty_name, terminal_id=terminal_id)
    check_native_parent(parent_channel)
    profile = os.environ.get("DEMO_TERMINAL_PROFILE", "")
    apple(SELECT_TAB, tty_name, terminal_id, profile)
    check_native_parent(parent_channel)
    expected = terminal_state(tty_name)
    if expected[9] != tty_name or not expected[10] or expected[6] < 16:
        raise RecordingError("the invoking Terminal tab must be selected/frontmost with an actual font of at least 16 pt")
    if expected[7] < 1 or expected[8] < 1:
        raise RecordingError("Terminal reported invalid selected-tab rows or columns")
    viewport = query_tty_geometry(tty_name)
    check_native_parent(parent_channel)
    if viewport[4:] != expected[7:9]:
        raise RecordingError("Terminal window-operation rows/columns do not match the selected owned tab")
    if not Path("/usr/bin/swiftc").is_file():
        raise RecordingError("Xcode Command Line Tools with swiftc are required for ScreenCaptureKit")

    lease.stop_confirmed = False
    owned_paths: dict[str, Path] = {}
    capture_dir: Path | None = None
    work: Path | None = None
    recording: Path | None = None
    demo_lifecycle: Path | None = None
    lifecycle_created = False
    ready: Path | None = None
    recorder: subprocess.Popen[bytes] | None = None
    demo_child: subprocess.Popen[bytes] | None = None
    published = False
    primary_status: int | None = None
    failure: str | None = None
    uncertain = False
    cleanup_failed = False
    try:
        check_native_parent(parent_channel)
        output.parent.mkdir(parents=True, exist_ok=True)
        capture_dir = create_tracked_tempdir(
            lease, owned_paths, "capture_dir", ".taskdemo-recording-", output.parent,
        )
        recording = capture_dir / "recording.mp4"
        owned_paths["recording"] = recording
        lease.update(recording=str(recording))
        work = create_tracked_tempdir(lease, owned_paths, "work", "taskdemo-sck-")
        demo_lifecycle = work / "demo-tmux-state"
        create_tracked_text_file(
            lease, owned_paths, "private_workload_state", demo_lifecycle, "idle\n",
        )
        lifecycle_created = True
        ready = work / "capture-ready"
        lease.update(ready=str(ready))
        assert work is not None and recording is not None and demo_lifecycle is not None and ready is not None
        swift = work / "record-window"
        check_native_parent(parent_channel)
        compiler = lease.spawn(
            "compiler",
            ["/usr/bin/swiftc", str(SCRIPT.with_name("record-window.swift")), "-o", str(swift)],
            env=public_environment(),
        )
        compile_deadline = time.monotonic() + ready_timeout
        while True:
            check_native_parent(parent_channel)
            remaining = compile_deadline - time.monotonic()
            if remaining <= 0:
                raise RecordingError("ScreenCaptureKit compiler exceeded CAPTURE_READY_TIMEOUT")
            compiler_status = wait_child(compiler, min(0.1, remaining))
            if compiler_status is not None:
                break
        if compiler_status != 0:
            raise RecordingError(f"ScreenCaptureKit compiler exited with status {compiler_status}")
        # Do not capture the invoking shell's prompt, launch command, or prior output.
        sys.stdout.write("\033[2J\033[H")
        sys.stdout.flush()
        recorder = lease.spawn(
            "recorder",
            [
                str(swift), str(recording), str(ready),
                *(str(value) for value in viewport),
                *(str(value) for value in expected[2:6]),
                terminal_id,
            ],
            env=public_environment(),
        )
        ready_deadline = time.monotonic() + ready_timeout
        while not ready.is_file():
            check_native_parent(parent_channel)
            if recorder.poll() is not None:
                raise RecordingError(f"ScreenCaptureKit exited before readiness (status {recorder.returncode})")
            if time.monotonic() >= ready_deadline:
                raise RecordingError("ScreenCaptureKit did not become ready before CAPTURE_READY_TIMEOUT")
            time.sleep(0.1)
        if recorder.poll() is not None:
            raise RecordingError(f"ScreenCaptureKit exited after readiness (status {recorder.returncode})")
        check_native_parent(parent_channel)
        check_window(tty_name, expected)
        print("ScreenCaptureKit is ready; running the demo in the selected Terminal tab content.", flush=True)
        demo_env = os.environ.copy()
        demo_env["DEMO_PRIVATE_TMUX_STATE_FILE"] = str(demo_lifecycle)
        check_native_parent(parent_channel)
        demo_child = lease.spawn("demo", ["bash", str(demo)], cwd=repo, env=demo_env, close_fds=True)
        deadline = time.monotonic() + timeout
        while True:
            check_native_parent(parent_channel)
            demo_status = demo_child.poll()
            if demo_status is not None:
                primary_status = demo_status
                if recorder.poll() is not None:
                    raise RecordingError(f"ScreenCaptureKit failed during demo (status {recorder.returncode})")
                break
            if recorder.poll() is not None:
                raise RecordingError(f"ScreenCaptureKit failed during demo (status {recorder.returncode})")
            if time.monotonic() >= deadline:
                raise RecordingError(f"demo exceeded TIMEOUT={timeout}s; recording is not accepted")
            check_window(tty_name, expected)
            time.sleep(0.5)
        # Latch the demo's actual exit before any recorder finalization can fail.
        check_native_parent(parent_channel)
        check_window(tty_name, expected)
        check_private_workload(demo_lifecycle)
        recorder_status = stop_recorder(recorder)
        recorder = None
        check_window(tty_name, expected)
        check_native_parent(parent_channel)
        if primary_status != 0:
            failure = f"demo exited with status {primary_status}; failed recording retained"
            return primary_status if primary_status > 0 else 128 - primary_status
        if recorder_status != 0:
            raise RecordingError(f"ScreenCaptureKit exited with status {recorder_status}; failed recording retained")
        if not recording.is_file() or recording.stat().st_size == 0:
            raise RecordingError("ScreenCaptureKit produced no nonempty recording")
        check_native_parent(parent_channel)
        # Hard-link publication is atomic and refuses to replace files or dangling aliases.
        os.link(recording, output)
        published = True
        print(f"recorded: {output} ({output.stat().st_size} bytes)")
        if week and day:
            print(f"suggested remote: /ai-advent-challenge/week-{week}/day-{day}-demo.mp4")
        return 0
    except (OSError, subprocess.SubprocessError, ValueError, RecordingError) as exc:
        failure = str(exc)
        if primary_status is None and demo_child is not None:
            primary_status = demo_child.poll()
        if primary_status not in (None, 0):
            return primary_status if primary_status > 0 else 128 - primary_status
        return 1
    finally:
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        # spawn() retains handles before unblocking signals, even if assignment was interrupted.
        demo_child = lease.children.get("demo")
        recorder = lease.children.get("recorder")
        compiler = lease.children.get("compiler")
        if compiler is not None and compiler.poll() is None:
            try:
                compiler.kill()
                if wait_child(compiler, JOIN_SECONDS) is None:
                    uncertain = True
                    failure = (failure + "; " if failure else "") + "compiler shutdown is unconfirmed"
            except (OSError, subprocess.SubprocessError) as exc:
                uncertain = True
                failure = (failure + "; " if failure else "") + f"compiler shutdown failed: {exc}"
        if demo_child is not None and demo_child.poll() is None:
            try:
                stopped = stop_demo(demo_child)
                if not stopped:
                    uncertain = True
                failure = (failure + "; " if failure else "") + (
                    "demo was interrupted; direct-child shutdown confirmed"
                    if stopped else "demo direct-child shutdown is unconfirmed"
                )
            except (OSError, subprocess.SubprocessError) as exc:
                uncertain = True
                failure = (failure + "; " if failure else "") + f"demo shutdown failed: {exc}"
        recorder_status: int | None = None
        if recorder is not None:
            try:
                recorder_status = stop_recorder(recorder)
            except (OSError, subprocess.SubprocessError, RecordingError) as exc:
                uncertain = True
                failure = (failure + "; " if failure else "") + str(exc)
        capture_dir = capture_dir or owned_paths.get("capture_dir")
        work = work or owned_paths.get("work")
        recording = recording or owned_paths.get("recording")
        demo_lifecycle = demo_lifecycle or owned_paths.get("private_workload_state")
        if lifecycle_created and demo_lifecycle is not None:
            try:
                check_private_workload(demo_lifecycle)
            except RecordingError as exc:
                uncertain = True
                failure = (failure + "; " if failure else "") + str(exc)
        if uncertain:
            lease.stop_confirmed = False
        else:
            if work is not None:
                try:
                    shutil.rmtree(work)
                except OSError as exc:
                    uncertain = True
                    cleanup_failed = True
                    lease.stop_confirmed = False
                    failure = (failure + "; " if failure else "") + f"private work cleanup failed at {work}: {exc}"
            if not uncertain and capture_dir is not None:
                if published:
                    try:
                        shutil.rmtree(capture_dir)
                    except OSError as exc:
                        uncertain = True
                        cleanup_failed = True
                        failure = (failure + "; " if failure else "") + (
                            f"published capture cleanup failed at {capture_dir}: {exc}"
                        )
                elif recording is not None and recording.exists():
                    print(f"failed take retained at {recording}", file=sys.stderr)
                else:
                    try:
                        shutil.rmtree(capture_dir)
                    except OSError as exc:
                        uncertain = True
                        cleanup_failed = True
                        failure = (failure + "; " if failure else "") + (
                            f"failed capture cleanup failed at {capture_dir}: {exc}"
                        )
            lease.stop_confirmed = not uncertain
        if failure:
            if published:
                print(f"error: {failure}; output may already be published at {output}", file=sys.stderr)
            else:
                print(f"error: {failure}; take not accepted", file=sys.stderr)
        if uncertain:
            retained_paths = [str(path) for path in (work, capture_dir) if path is not None]
            print(
                "unconfirmed finalization; reconcile retained paths "
                + (", ".join(retained_paths) if retained_paths else str(SCREEN_LOCK)),
                file=sys.stderr,
            )
        if cleanup_failed or uncertain:
            retained_paths = [str(path) for path in (work, capture_dir) if path is not None]
            retained = ", ".join(retained_paths) if retained_paths else str(SCREEN_LOCK)
            raise RecordingError(failure or f"capture finalization is unconfirmed; retain {retained}")

def main() -> int:
    if len(sys.argv) == 2 and sys.argv[1] == "--permission-preflight":
        return permission_preflight()
    native = len(sys.argv) == 5 and sys.argv[1] == "--native-child"
    if not native and len(sys.argv) not in (2, 3):
        print("usage: record.sh <demo-script> [output.mp4]", file=sys.stderr)
        return 2
    signal.signal(signal.SIGINT, interrupt_supervisor)
    signal.signal(signal.SIGTERM, interrupt_supervisor)
    try:
        if native:
            return native_child(sys.argv[2], sys.argv[3], sys.argv[4])
        return run(sys.argv[1], sys.argv[2] if len(sys.argv) == 3 else None)
    except ShutdownRequested as exc:
        print(f"error: {exc.reason}; supervisor exiting with status {128 + exc.signum}", file=sys.stderr)
        return 128 + exc.signum
    except (RecordingError, OSError, subprocess.SubprocessError, ValueError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    except KeyboardInterrupt:
        print("error: interrupted; supervisor exiting with status 130", file=sys.stderr)
        return 130


if __name__ == "__main__":
    raise SystemExit(main())
