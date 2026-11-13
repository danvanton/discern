# makemkvcon test data

Robot-mode output (`makemkvcon -r`) used by the tests in `internal/makemkv`.

| File | What it is |
|---|---|
| `info-demo-small.txt` | **Hand-written** for the README's demo: a 40 MiB "main feature" (title 0, labelled `MATRIX_THE`), an extra and a short clip, so a stand-in rip of matching size passes Verify. |
| `info-sample-movie.txt` | **Hand-written** to the documented format, not recorded from a real disc. It is a Blu-ray with a main feature, an extra, a play-all of the extras and a 16-second clip, plus a title name with an escaped quote and a comma. |

Recorded scans from real discs will be added next to it, named `info-<label>.txt`. They are the better test data: a quirk a real disc shows becomes a test case here. Record one with:

```sh
makemkvcon -r --cache=1 info disc:0 --minlength=0 > info-<label>.txt
```

Files here are marked `-text` in `.gitattributes`, so git never changes their line endings.
