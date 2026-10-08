Phase: planning.

Goal: break the work into small tasks and delegate them with `create_tasks`. Each task goes to an executor that sees only its own instructions, so write them complete: what to do, where, the constraints, and how to know it is finished.

Keep tasks small enough for one focused session. Tasks that do not depend on each other run in parallel; use `depends_on` where one needs another's result. Tasks that change the repository run one at a time, in dependency order.
