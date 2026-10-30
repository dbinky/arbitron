# Shared workflow library (`_lib`)

Files in this folder are the shared library that every workflow can reference.
They are seeded once into the shared library when Arbitron boots, and they ship
in the samples tarball. The folder name `_lib` is reserved: it is never listed
or seeded as a workflow.

## Referencing a library file

Use the `@lib/` prefix (case-sensitive) wherever a workflow takes a file path,
for example a node's template file:

    @lib/prompts/review.md

The path after `@lib/` is relative to the library root. Traversal (`..`),
absolute paths, backslashes and empty components are rejected, and the names
`workflow.json` and `layout.json` are reserved at the top level.

## Private, then shared

There are two libraries: one per user (private) and one shared. A reference
resolves in the caller's private library first and falls back to the shared
library. A private file with the same relative path therefore shadows the
shared one for its owner.

## Lib-relative includes

A library file that includes another file resolves the include relative to the
library root, not to the including workflow. Write includes as paths inside the
library and they behave the same no matter which workflow pulls them in.

## Live reads

Library files are read at run time, not snapshotted into an instance. Editing a
file changes every workflow that references it, including instances that are
already running. A broken edit breaks those running instances, so treat the
shared library like shared code.

## Seeding and changes

Each file here is seeded at most once. A file an admin has since edited or
deleted is never overwritten or recreated by a later boot, and a file that
already exists in the shared library is adopted as-is. New files in a later
release are added.

Change files in this folder through pull requests, with tests, not by hand on a
running server. Only admins may edit the shared library at run time, and every
such edit is audited.
