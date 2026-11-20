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


ABI = {"pointer_bytes": 8, "long_double_bytes": 16, "long_double_mantissa_bits": 64,
       "pointer_record_bytes": 16, "rb_node_bytes": 40, "ndebug": 1}


def verify_driver(driver, instrumented):
    """Identify the native role before trusting semantic comparison evidence."""
    from transport import run, decode_rows
    result = run([str(driver), '--abi'], timeout=5)
    assert result.returncode == 0, ('native ABI exit', str(driver), result.returncode, result.stderr)
    rows = decode_rows(result.stdout)
    assert rows == [ABI | {'instrumented': int(instrumented)}], ('native ABI/role mismatch', str(driver), rows)


def verify_pair(driver, comparison):
    assert pathlib.Path(driver).resolve() != pathlib.Path(comparison).resolve(), 'comparison must be an identified unmodified executable'
    verify_driver(driver, True)
    verify_driver(comparison, False)


def native_run(driver, *, instrumented, options=(), **kwargs):
    # Synthetic transport controls call transport.run directly and never claim
    # to be native semantic evidence.
    from transport import run
    verify_driver(driver, instrumented)
    return run([str(driver), *options], **kwargs)
