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
import binascii
import json
import os
import sys
import xxtea
from pathlib import Path
from cryptography.hazmat.primitives import hashes
from cryptography.hazmat.primitives.asymmetric import padding
from cryptography.hazmat.backends import default_backend
from cryptography.hazmat.primitives import serialization


__author__ = '@sourcefrenchy'
__copyright__ = 'none'
__email__ = 'jeanmichel.amblat@gmail.com'
__status__ = 'PROTOTYPE'


class Subcipher(object):
    """Encoding/Decoding operations."""
    def __init__(self, spot):
        """Constructor."""
        self.spotipy = spot
        self.public_key = self.load_public_key()
        self.private_key = self.load_private_key()

    def load_public_key(self):
        """Load local public key"""
        with open("spotexfil.id_rsa.pub.pem", "rb") as key_file:
            pub = serialization.load_pem_public_key(
                key_file.read(),
                backend=default_backend()
            )
        print("[*] RSA pub key loaded")
        return pub

    def load_private_key(self):
        """Load local private key"""
        with open("spotexfil.id_rsa.pem", "rb") as key_file:
            priv = serialization.load_pem_private_key(
                key_file.read(),
                password=None,
                backend=default_backend()
            )
        print("[*] RSA priv key loaded")
        return priv

    def rsa_encrypt(self, message):
        encrypted = self.public_key.encrypt(
            message,
            padding.OAEP(
                mgf=padding.MGF1(algorithm=hashes.SHA256()),
                algorithm=hashes.SHA256(),
                label=None
            )
        )
        return binascii.b2a_base64(encrypted)

    def rsa_decrypt(self, message):
        message = binascii.a2b_base64(message)
        original_message = self.private_key.decrypt(
            message,
            padding.OAEP(
                mgf=padding.MGF1(algorithm=hashes.SHA256()),
                algorithm=hashes.SHA256(),
                label=None
            )
        )
        return original_message

    def encode_payload(self, input_file):
        """Encoding text."""
        try:
            with open(input_file, "rb") as input_data:
                plaintext = input_data.read()
        except OSError as err:
            print("[!] Cannot read file: {}".format(err))
            sys.exit(0)
        ct = self.rsa_encrypt(plaintext)
        print("[*] ct={}".format(ct))
        encoded = json.dumps(ct.decode())
        return encoded

    def decode_payload(self, payload):
        """Decoding text."""
        try:
            unescaped = html.unescape(payload)
            print("[!] unescaped={}".format(unescaped))
            encoded = json.loads(unescaped)
            ct = self.rsa_decrypt(encoded.encode())
        except Exception:
            print("[!] Failure to retrieve data or bad key: {}".format(Exception))
            sys.exit(0)
        return bytes(ct).decode('utf-8')[:-1]
