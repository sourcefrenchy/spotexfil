"""interfaces.py - Abstract base classes for SpotExfil components.

Defines the contracts that crypto, transport, and C2 module
implementations must satisfy.
"""

from abc import ABC, abstractmethod


class CryptoProvider(ABC):
    """Interface for payload encryption and encoding."""

    @abstractmethod
    def encode_payload(self, input_file: str) -> str:
        """Encode a file into a transmittable payload string.

        Args:
            input_file: Path to the file to encode.

        Returns:
            Encoded payload string ready for chunking.
        """

    @abstractmethod
    def decode_payload(self, payload: str) -> bytes:
        """Decode a received payload back to original file bytes.

        Args:
            payload: The raw concatenated playlist descriptions.

        Returns:
            Original file bytes.
        """


class Transport(ABC):
    """Interface for covert channel transport."""

    @abstractmethod
    def generate_playlists(self, payload: str):
        """Chunk payload and store via the transport medium.

        Args:
            payload: The encoded payload string to transmit.
        """

    @abstractmethod
    def retrieve_playlists(self) -> str:
        """Retrieve and reassemble payload from the transport medium.

        Returns:
            Concatenated payload string from all matching playlists.
        """

    @abstractmethod
    def clear_data(self):
        """Delete all payload data from the transport medium."""


class C2Module(ABC):
    """Interface for C2 implant modules."""

    @property
    @abstractmethod
    def name(self) -> str:
        """Module name used for dispatch."""

    @abstractmethod
    def execute(self, args: dict) -> tuple:
        """Execute the module with given arguments.

        Args:
            args: Module-specific arguments dict.

        Returns:
            Tuple of (status: str, data: str).
        """
