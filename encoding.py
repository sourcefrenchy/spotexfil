"""encoding.py - Class for encoding/decoding operations.
    Calling this Subcipher is rather aspirational :P

Todo:
    * Leverage compression for large paylaods (smaz for text,
        gzip for binaries for ex.)
"""

import html
import json
from os import unsetenv
import sys
import base64
from pathlib import Path
import mimetypes
from hashlib import blake2b
import re


__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jmamblat@icloud.com'
__status__ = 'PROTOTYPE'


class Subcipher(object):
    """Encoding/Decoding operations."""
    def __init__(self, spot):
        """Constructor."""
        self.spotipy = spot

    def encode_payload(self, input_file):
        """Encoding text."""
        try:
             plaintext = Path(input_file).read_bytes()
        except OSError as err:
            print("[!] Cannot read file: {}".format(err))
            sys.exit(0)
        
        h = blake2b(digest_size=20)
        h.update(plaintext)
        print("[*] checksum plaintext {}".format(h.hexdigest()))
        # if mimetypes.guess_type(input_file) == (None, None): # check if text or binary
        #     print("\n[D] TXT PAYLOAD BEFORE JSON=|{}|\n".format(base64.b64encode(plaintext).decode('utf-8')))
        #     b64data = base64.b64encode(plaintext)
        #     b64datadec = b64data.decode('utf-8')
        # else:
        #     print("\n[D] BIN PAYLOAD BEFORE JSON=|{}|\n".format(base64.encodebytes(plaintext).decode('utf-8')))
        #     b64data = base64.encodebytes(plaintext)
        #     b64datadec = b64data.decode('utf-8')
        b64data = base64.b64encode(plaintext)
        b64datadec = b64data.decode('utf-8')
        return json.dumps(b64datadec)

    def decode_payload(self, payload):
        """Decoding text. Don't forget unescape first."""
        def decode_base64(data, altchars=b'+/'):
            """Decode base64, padding being optional.

            :param data: Base64 data as an ASCII byte string
            :returns: The decoded byte string.

            """
            data = re.sub(rb'[^a-zA-Z0-9%s]+' % altchars, b'', data)  # normalize
            missing_padding = len(data) % 4
            if missing_padding:
                data += b'='* (4 - missing_padding)
            return base64.b64decode(data, altchars)
        try:
            data = html.unescape(payload)#.replace("\\n", "\n")[:-2]
        except Exception:
            print("[!] Failure // type(data)={} data=|{}|\nEXCEPTIONS --> {}".format(type(data), data, Exception))
            sys.exit(0)
        
        try:
            b64datenc = json.loads(data)
        except Exception:
            print("[D] json.loads fail dataretrieved=|{}|".format(data))
            sys.exit(0)
        
        b64data = b64datenc.encode('utf-8')
        
        # try: # check if text or binary
        #     plaintext = base64.decodebytes(b64data)
        plaintext = decode_base64(b64data)
        # except:
        #     plaintext = base64.decodebytes(b64data)
        #     print("[D] PAYLOAD b64data=|{}|".format(b64data))
            
        h = blake2b(digest_size=20)
        h.update(plaintext)
        print("[*] checksum payload {}".format(h.hexdigest()))
        return plaintext
