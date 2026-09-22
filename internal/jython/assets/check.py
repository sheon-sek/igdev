# igdev's Jython compatibility checker: compile every file named on the command
# line in one interpreter, so N files cost one JVM launch rather than N.
#
# It is invoked by internal/jython as:
#
#   java -jar jython-standalone-<version>.jar -c "<this script>" <file>...
#
# Every failure is printed as "<path>:<line>: <message>" on stderr, which is the
# contract the Go side parses. All files are checked before the exit status says
# whether any of them failed, so one run reports every problem it found.
#
# Jython 2.7 is the interpreter target, so this file stays Python 2 syntax while
# also being valid Python 3: the tests emulate the JVM with python3 when no real
# JVM is available.

import sys


def problem_with(path):
    """Return the diagnostic for one file, or None when it compiles."""
    try:
        handle = open(path, "rb")
        try:
            source = handle.read()
        finally:
            handle.close()
    except Exception as exc:
        return "%s: %s" % (path, exc)
    try:
        compile(source, path, "exec")
    except SyntaxError as exc:
        line = exc.lineno or 0
        message = exc.msg or "syntax error"
        return "%s:%d: %s" % (path, line, message)
    except Exception as exc:
        return "%s: %s" % (path, exc)
    return None


def main(paths):
    failed = 0
    for path in paths:
        problem = problem_with(path)
        if problem is None:
            continue
        failed = 1
        sys.stderr.write(problem + "\n")
    sys.stderr.flush()
    return failed


sys.exit(main(sys.argv[1:]))
