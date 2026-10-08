// Questions the workflow put to the human, and which comment answers each.

export interface TaskComment {
    id: number;
    author_type: string;
    comment_type?: string;
    reply_to_id?: number | null;
    content: string;
    created_at: string;
    [key: string]: unknown;
}

export interface PairedQuestions {
    // question id → the comment that answers it
    replies: Map<number, TaskComment>;
    // ids of the comments that are answers, shown under their question
    replyIds: Set<number>;
    // questions still waiting for an answer, oldest first
    open: TaskComment[];
}

const isPlainHumanComment = (comment: TaskComment): boolean =>
    comment.author_type === 'human' && !comment.comment_type;

// pairQuestions matches answers to questions the way the workflow does: a
// reply that names its question always answers it; a plain comment answers
// only the oldest question still open, and only once.
export function pairQuestions(comments: TaskComment[]): PairedQuestions {
    const ordered = [...comments].sort((a, b) => a.id - b.id);
    const questions = ordered.filter(comment => comment.comment_type === 'ask_user');
    const replies = new Map<number, TaskComment>();
    const replyIds = new Set<number>();

    for (const comment of ordered) {
        if (comment.author_type !== 'human' || comment.reply_to_id == null) continue;
        if (questions.some(question => question.id === comment.reply_to_id) && !replies.has(comment.reply_to_id)) {
            replies.set(comment.reply_to_id, comment);
            replyIds.add(comment.id);
        }
    }
    for (const comment of ordered) {
        if (!isPlainHumanComment(comment) || comment.reply_to_id != null) continue;
        const oldestOpen = questions.find(question => !replies.has(question.id));
        if (oldestOpen && comment.id > oldestOpen.id) {
            replies.set(oldestOpen.id, comment);
            replyIds.add(comment.id);
        }
    }
    return { replies, replyIds, open: questions.filter(question => !replies.has(question.id)) };
}
