"""File operation tools.

Following claw_agent_engine's FileTools pattern:
- read_file: Read file contents
- write_file: Write contents to file
- search_files: Search files using regex
"""

from __future__ import annotations

import os
import re
from typing import Any

from .base import BaseTool, ToolParameter, ToolResult


class FileReadTool(BaseTool):
    """Read file contents."""

    @property
    def name(self) -> str:
        return "read_file"

    @property
    def description(self) -> str:
        return "Read the contents of a file at the specified path."

    @property
    def parameters(self) -> list[ToolParameter]:
        return [
            ToolParameter(
                name="path",
                type="string",
                description="Path to the file to read",
                required=True,
            ),
            ToolParameter(
                name="start_line",
                type="integer",
                description="Starting line number (1-based, default: 1)",
                required=False,
            ),
            ToolParameter(
                name="end_line",
                type="integer",
                description="Ending line number (1-based, default: 2000)",
                required=False,
            ),
        ]

    def execute(self, path: str, start_line: int = 1, end_line: int = 2000, **kwargs) -> ToolResult:
        try:
            if not os.path.exists(path):
                return ToolResult.error(f"File not found: {path}")

            with open(path, "r", encoding="utf-8") as f:
                lines = f.readlines()

            # Convert to 0-based indexing
            start = max(0, start_line - 1)
            end = min(len(lines), end_line)

            content = "".join(lines[start:end])
            total_lines = len(lines)

            structured = {
                "path": path,
                "total_lines": total_lines,
                "lines_returned": end - start,
                "start_line": start_line,
                "end_line": end_line,
            }
            return ToolResult.success(content, structured=structured)

        except UnicodeDecodeError:
            return ToolResult.error(f"Cannot read binary file: {path}")
        except PermissionError:
            return ToolResult.error(f"Permission denied: {path}")
        except Exception as exc:
            return ToolResult.error(f"Failed to read file: {exc}")


class FileWriteTool(BaseTool):
    """Write contents to a file."""

    @property
    def name(self) -> str:
        return "write_file"

    @property
    def description(self) -> str:
        return "Write content to a file at the specified path. Creates directories if needed."

    @property
    def parameters(self) -> list[ToolParameter]:
        return [
            ToolParameter(
                name="path",
                type="string",
                description="Path to the file to write",
                required=True,
            ),
            ToolParameter(
                name="content",
                type="string",
                description="Content to write to the file",
                required=True,
            ),
        ]

    def execute(self, path: str, content: str, **kwargs) -> ToolResult:
        try:
            # Create directories if needed
            directory = os.path.dirname(path)
            if directory:
                os.makedirs(directory, exist_ok=True)

            with open(path, "w", encoding="utf-8") as f:
                f.write(content)

            return ToolResult.success(f"Successfully wrote {len(content)} characters to {path}")

        except PermissionError:
            return ToolResult.error(f"Permission denied: {path}")
        except Exception as exc:
            return ToolResult.error(f"Failed to write file: {exc}")


class FileSearchTool(BaseTool):
    """Search files using regex pattern."""

    @property
    def name(self) -> str:
        return "search_files"

    @property
    def description(self) -> str:
        return "Search for a regex pattern in files within a directory."

    @property
    def parameters(self) -> list[ToolParameter]:
        return [
            ToolParameter(
                name="path",
                type="string",
                description="Directory path to search in",
                required=True,
            ),
            ToolParameter(
                name="pattern",
                type="string",
                description="Regular expression pattern to search for",
                required=True,
            ),
            ToolParameter(
                name="file_pattern",
                type="string",
                description="Glob pattern to filter files (e.g., '*.py')",
                required=False,
            ),
        ]

    def execute(
        self,
        path: str,
        pattern: str,
        file_pattern: str = "*",
        **kwargs,
    ) -> ToolResult:
        try:
            if not os.path.isdir(path):
                return ToolResult.error(f"Directory not found: {path}")

            import fnmatch
            results = []

            for root, _dirs, files in os.walk(path):
                for filename in files:
                    if not fnmatch.fnmatch(filename, file_pattern):
                        continue

                    filepath = os.path.join(root, filename)
                    try:
                        with open(filepath, "r", encoding="utf-8") as f:
                            for line_num, line in enumerate(f, 1):
                                if re.search(pattern, line):
                                    results.append({
                                        "file": filepath,
                                        "line": line_num,
                                        "content": line.rstrip(),
                                    })
                    except (UnicodeDecodeError, PermissionError):
                        continue

            return ToolResult.success(
                f"Found {len(results)} matches for '{pattern}' in {path}",
                structured={"matches": results, "count": len(results)},
            )

        except Exception as exc:
            return ToolResult.error(f"Failed to search files: {exc}")