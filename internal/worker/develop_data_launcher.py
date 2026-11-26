"""Enable DevelopData for this run without changing the agent's debug mode."""

import os
import runpy
import sys

# The launcher lives under /work, while agent modules live under /app.
sys.path.insert(0, os.getcwd())

from adf_core_python.core.agent.develop.develop_data import DevelopData

develop_file = sys.argv.pop(1)
original_init = DevelopData.__init__
used = False


def init_with_experiment_data(self, is_develop_mode=False, file_name=DevelopData.DEFAULT_FILE_NAME):
    global used
    if file_name == develop_file:
        used = True
        is_develop_mode = True
    original_init(self, is_develop_mode, file_name)


# ADF Core Python through v0.2.15 uses the debug flag in its connectors.
# Only intercept the generated file so other DevelopData instances are untouched.
DevelopData.__init__ = init_with_experiment_data
sys.argv[0] = "main.py"
runpy.run_path("main.py", run_name="__main__")
if not used:
    raise RuntimeError("agent did not load the experiment DevelopData file")
