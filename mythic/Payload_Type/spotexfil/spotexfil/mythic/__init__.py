import glob
import os.path
import sys
from importlib import import_module, invalidate_caches
from pathlib import Path

# Auto-import every command module in agent_functions so their CommandBase
# subclasses (and the PayloadType in builder.py) register with
# mythic_container at import time. Mirrors the Athena/Poseidon pattern.
currentPath = Path(__file__)
searchPath = currentPath.parent / "agent_functions" / "*.py"
modules = glob.glob(f"{searchPath}")
invalidate_caches()
for x in modules:
    if not x.endswith("__init__.py") and x[-3:] == ".py":
        module = import_module(f"{__name__}.agent_functions." + Path(x).stem)
        for el in dir(module):
            if "__" not in el:
                globals()[el] = getattr(module, el)

sys.path.append(os.path.abspath(currentPath.name))
