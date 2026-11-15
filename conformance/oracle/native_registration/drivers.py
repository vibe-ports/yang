# SPDX-License-Identifier: BSD-3-Clause
"""Shared native comparison-driver selection; explicit missing paths still fail."""
import os
import pathlib


def comparison_driver(driver=None, comparison=None):
    selected = comparison or os.environ.get('YANG_NATIVE_REGISTRATION_COMPARISON_DRIVER')
    if selected:
        return pathlib.Path(selected).absolute()
    driver = driver or os.environ.get('YANG_NATIVE_REGISTRATION_DRIVER')
    if driver:
        return pathlib.Path(driver).absolute().with_name('driver-unmodified')
    return None
