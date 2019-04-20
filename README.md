# spotexfil (status: MVP)
A simple way to exfiltrate data using spotify API, 300 bytes at a time.

We can read a file (payload) and encode it inside a playlist description field via Spotifx API.

# TODO
* iterate if payload larger than 300 bytes and create additional playlists
* move from XXTEA crappy easy crypto to asymmetric (i.e. curve 25519)
* split into client (hold pub key) and listener/server (hold priv key)

