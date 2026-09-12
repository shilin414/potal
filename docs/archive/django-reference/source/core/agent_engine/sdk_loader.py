"""Locate and import the sibling GraphFlow Agent Engine Python SDK.

The native SDK is built in the GraphFlow repository, so it cannot be copied as
ordinary Python source without also shipping its platform-specific extension
and DLLs.  This loader keeps the studio configurable while supporting the
default sibling-repository layout used by this workspace.
"""
from __future__ import annotations

import logging
import sys
from pathlib import Path

from django.conf import settings

logger = logging.getLogger(__name__)


def graphflow_sdk_path() -> Path:
    path = Path(settings.GRAPHFLOW_SDK_PATH).expanduser().resolve()
    package = path / "graphflow_agent_engine"
    if not package.is_dir():
        logger.error(
            "GraphFlow Agent Engine SDK not found at %s "
            "(expected package dir %s; GRAPHFLOW_SDK_PATH=%s)",
            path, package, settings.GRAPHFLOW_SDK_PATH,
        )
        raise RuntimeError(
            "GraphFlow Agent Engine SDK was not found at "
            f"{path}. Set GRAPHFLOW_SDK_PATH to GraphFlow/sdk/python."
        )
    logger.debug("Resolved GraphFlow SDK path: %s", path)
    return path


def load_sdk():
    """Return the ``graphflow_agent_engine`` module from the configured SDK."""
    sdk_path = graphflow_sdk_path()
    sdk_string = str(sdk_path)
    if sdk_string not in sys.path:
        sys.path.insert(0, sdk_string)
        logger.debug("Inserted GraphFlow SDK into sys.path: %s", sdk_string)

    try:
        import graphflow_agent_engine
    except (ImportError, OSError) as exc:
        logger.exception(
            "Failed to load GraphFlow Agent Engine native runtime from %s", sdk_path,
        )
        raise RuntimeError(
            "GraphFlow Agent Engine could not load its Python/native runtime "
            f"from {sdk_path}: {exc}"
        ) from exc

    # Initialize the SDK's process-global C++/spdlog logger so the engine's
    # LOG_* calls actually emit. The logger is a no-op until this runs, so the
    # SDK is otherwise silent. Mirrors agent_engine_gui.py's main() startup.
    # Idempotent and must run BEFORE constructing Manager/Engine (the next call
    # site after load_sdk()).
    level = getattr(settings, "GRAPHFLOW_LOG_LEVEL", "info")
    if level:
        try:
            graphflow_agent_engine.initialize_logging(level)
            logger.info(
                "GraphFlow native logging initialized at level=%s", level,
            )
        except Exception as exc:  # ValueError on bad level; AgentEngineError if native missing
            # Unknown level or native logger unavailable — don't block startup.
            logger.warning(
                "initialize_logging(%r) failed (engine logs will be silent): %s",
                level, exc,
            )

    logger.info(
        "GraphFlow Agent Engine SDK loaded from %s (version=%s)",
        sdk_path,
        getattr(graphflow_agent_engine, "__version__", "unknown"),
    )
    return graphflow_agent_engine
