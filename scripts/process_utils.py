"""Process inspection for Linux and macOS validation, without extra packages."""
import os
from pathlib import Path
import subprocess
import sys


def _ps(pid, fields):
    result = subprocess.run(
        ["/bin/ps", "-p", str(int(pid)), "-o", fields],
        text=True, capture_output=True, env={**os.environ, "LC_ALL": "C"})
    if result.returncode == 1 and not result.stdout.strip() and not result.stderr.strip():
        raise ProcessLookupError(pid)
    if result.returncode != 0:
        raise OSError(result.stderr or "ps failed")
    return result.stdout.strip()


def process_alive(pid):
    try:
        if sys.platform == "darwin":
            return not _ps(pid, "stat=").startswith("Z")
        fields = Path(f"/proc/{int(pid)}/stat").read_text().rsplit(")", 1)[1].split()
        return fields[0] not in ("Z", "X")
    except (FileNotFoundError, ProcessLookupError):
        return False


def cpu_seconds(value):
    """Parse ps cumulative CPU time: [[days-]hours:]minutes:seconds."""
    days = 0
    if "-" in value:
        day, value = value.split("-", 1)
        days = int(day)
    total = 0.0
    for part in value.split(":"):
        total = total * 60 + float(part)
    return days * 86400 + total


def process_sample(pid):
    if sys.platform == "darwin":
        cpu, rss = _ps(pid, "time=,rss=").split()
        return cpu_seconds(cpu), int(rss) * 1024
    # comm may contain whitespace or closing parentheses.
    fields = Path(f"/proc/{int(pid)}/stat").read_text().rsplit(")", 1)[1].split()
    return ((int(fields[11]) + int(fields[12])) / os.sysconf("SC_CLK_TCK"),
            int(fields[21]) * os.sysconf("SC_PAGE_SIZE"))
