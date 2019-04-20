# -*- coding: utf-8 -*-
"""encoding.py - Class for encoding/decoding operations.
    Calling this Subcipher is rather aspirational :P

Written by: Jean-Michel Amblat (@Sourcefrenchy)
Status:     PROTOTYPE/UGLY ALPHA.

Todo:
    * xxtea was for quick testing and frankly to obfuscate more than encrypt.
        It is weak cipher (https://eprint.iacr.org/2010/254) and we should move to AES at one point.
    * Leverage compression for large paylaods (smaz for text, gzip for binaries for ex.)
    * Peer-review of code by a real Python dev to simplify/optimize!

"""

import html
import json
import os
import sys
import xxtea
from pathlib import Path


__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jeanmichel.amblat@gmail.com'
__status__ = 'PROTOTYPE'


class Subcipher(object):
    """Encoding/Decoding operations."""
    def __init__(self, spot):
        """Constructor."""
        self.spotipy = spot
        self.secret = self.generate_secret()

    def generate_secret(self):
        """If a local ./secret.used file exists, read & returns content.
        Otherwide, generate a random 16-bytes key and create that file.
        """
        secret_file = "./secret.used"
        secret_path = Path(secret_file)
        if secret_path.is_file():
            f = open(secret_file, "rb")
            secret = f.readline()
            f.close()
            return secret
        else:
            secret = os.urandom(16)
            os.umask(0o077)
            f = open(secret_file, "bw")
            f.write(secret)
            f.close()
            return secret

    def encode_payload(self, input_file):
        """Encoding text."""
        try:
            with open(input_file, "rb") as input_data:
                plaintext = input_data.read()
            if len(plaintext) > 300:
                print("[!] Payload too large (exceeding 300 bytes)."
                         "Will be fixed in a future release.")
                sys.exit(0)
        except OSError as err:
            print("[!] Cannot read file: {}".format(err))
            sys.exit(0)
        ct = xxtea.encrypt_hex(plaintext, self.secret)
        encoded = json.dumps(ct.decode())
        return encoded

    def decode_payload(self, payload):
        """Decoding text."""
        try:
            unescaped = html.unescape(payload)
            encoded = json.loads(unescaped)
            ct = xxtea.decrypt_hex(encoded.encode(), self.secret)
        except Exception:
            print("[!] Failure to retrieve data or bad key: {}".format(Exception))
            sys.exit(0)
        return bytes(ct).decode('utf-8')[:-1]