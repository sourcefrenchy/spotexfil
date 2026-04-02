"""C2 module registry.

Auto-registers all built-in modules. External modules can be added
via register_module().
"""

from .base import BaseModule  # noqa: F401
from .shell import ShellModule
from .exfil import ExfilModule
from .sysinfo import SysinfoModule

# Module registry: name -> module class
_REGISTRY = {}


def register_module(module_cls):
    """Register a C2 module class by its name property.

    Args:
        module_cls: A class implementing C2Module / BaseModule.
    """
    instance = module_cls()
    _REGISTRY[instance.name] = module_cls


def get_module(name: str):
    """Get a registered module class by name.

    Args:
        name: Module name string.

    Returns:
        Module class, or None if not found.
    """
    return _REGISTRY.get(name)


def list_modules() -> list:
    """Return list of registered module names."""
    return list(_REGISTRY.keys())


# Auto-register built-in modules
register_module(ShellModule)
register_module(ExfilModule)
register_module(SysinfoModule)
