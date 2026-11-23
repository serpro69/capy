# Explain which execution runtimes are available

**Status** Proposed · [Implementation](implementation.md) · [Tasks](tasks.md)

An operator sees that a language is missing from the execute tool and needs to know which executable capy looked for. Doctor currently reports the number of available languages and their names. The executor already has the ordered candidates for each of its eleven languages. A failure in preparation identifies the language but omits those candidate names.

## Operator contract

Both `capy doctor` and `capy_doctor` show the existing aggregate runtime health plus one stable row per supported language, ordered by language name. An available row names the executable selected by this process. An unavailable row lists the candidate names checked, in order, with a short instruction to make one available on the capy process's PATH. Keep a missing optional interpreter informational at row level. Preserve the aggregate convention of failure for zero, warning for one, and pass for two or more languages.

Use the same lookup result as the local executor. In a long-lived server the result reflects its cached detection. A fresh `capy doctor` may see a newly installed executable sooner; tell the operator to restart the server for a new sample. Display only candidate basenames and controlled diagnostic categories. Retain the selected executable's full path internally for execution. A usable shim should be invoked through the path that detection selected.

## Assumptions

The bundled language-to-candidate table answers the operator's first question. A successful PATH lookup cannot certify a compiler, its dependencies, or the health of captured execution on that machine.

## Not Doing

Runtime installation belongs to the operator. Invoking candidate binaries for version checks would add work and change what doctor measures.

## Rejected alternatives

A separate list in doctor would drift from execution. Printing the entire search path or raw OS errors would expose environment details without identifying which supported candidate to install.
