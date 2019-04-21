"""encoding.py - Class for encoding/decoding operations.
    Calling this Subcipher is rather aspirational :P

Todo:
    * xxtea was for quick testing and frankly to obfuscate more than encrypt.
        It is weak cipher (https://eprint.iacr.org/2010/254)
        and we could move to AES/RSA at one point.
    * Leverage compression for large paylaods (smaz for text,
        gzip for binaries for ex.)
"""

import html
import json
import sys
import xxtea


__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jeanmichel.amblat@gmail.com'
__status__ = 'PROTOTYPE'


class Subcipher(object):
    """Encoding/Decoding operations."""
    def __init__(self, spot):
        """Constructor."""
        self.spotipy = spot
        self.secret = "S.creTPassPhrase"[:16].encode()   # 16 bytes

    def encode_payload(self, input_file):
        """Encoding text."""
        try:
            with open(input_file, "rb") as input_data:
                plaintext = input_data.read()
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
            print("[!] Failure to retrieve data or bad key: {}"
                  .format(Exception))
            sys.exit(0)
        return bytes(ct).decode('utf-8')[:-1]
