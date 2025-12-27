Compiler Explorer in the Terminal with file watching!

## Example Usage

```sh
cet --help
cet -args="-O ReleaseFast -target aarch64-macos -mcpu=apple_m4" src/main.zig
```

![screenshot](./image.png)

## Future Plans

### Multi-File Project Support

The Compiler Explorer API supports multi-file compilation via a `files` array parameter:

```json
{
  "source": "<main file content>",
  "files": [
    {"filename": "header.hpp", "contents": "..."},
    {"filename": "utils.cpp", "contents": "..."}
  ],
  "options": { ... }
}
```

### CMake Support

A dedicated CMake endpoint exists at `POST /api/compiler/<compiler-id>/cmake`:

```json
{
  "source": "<CMakeLists.txt content>",
  "files": [
    {"filename": "main.cpp", "contents": "..."},
    {"filename": "utils.cpp", "contents": "..."},
    {"filename": "utils.hpp", "contents": "..."}
  ],
  "options": {
    "compilerOptions": {
      "cmakeArgs": "-DCMAKE_BUILD_TYPE=Release",
      "customOutputFilename": "myapp"
    }
  }
}
```

### Implementation Tasks

- Add `files` field to `CompileRequest` struct
- Watch a directory instead of a single file
- For CMake projects: use the `/cmake` endpoint with `CMakeLists.txt` as the source
- Collect all `.cpp`, `.hpp`, `.h` files in the project directory
