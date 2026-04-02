"""sysinfo.py - System information gathering module."""

import json
import os
import platform
import socket

from .base import BaseModule


class SysinfoModule(BaseModule):
    """Gather system information."""

    @property
    def name(self) -> str:
        return "sysinfo"

    def execute(self, args: dict) -> tuple:
        """Gather system information.

        Args:
            args: Unused for this module.

        Returns:
            Tuple of (status, json_encoded_info).
        """
        info = {
            "os": platform.platform(),
            "hostname": socket.gethostname(),
            "username": self._get_username(),
            "ips": self._get_ips(),
            "pid": os.getpid(),
            "cwd": os.getcwd(),
        }
        return ("ok", json.dumps(info, indent=2))

    @staticmethod
    def _get_username() -> str:
        """Get current username safely."""
        try:
            return os.getlogin()
        except OSError:
            import getpass
            return getpass.getuser()

    @staticmethod
    def _get_ips() -> list:
        """Get local IP addresses."""
        ips = []
        try:
            hostname = socket.gethostname()
            for info in socket.getaddrinfo(hostname, None):
                addr = info[4][0]
                if addr not in ips and not addr.startswith('::'):
                    ips.append(addr)
        except socket.gaierror:
            pass
        return ips
