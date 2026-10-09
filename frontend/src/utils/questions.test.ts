import { describe, expect, it } from 'vitest';
import { pairQuestions } from './questions';
import type { TaskComment } from './questions';

const comment = (id: number, fields: Partial<TaskComment>): TaskComment => ({
    id, author_type: 'agent', content: `c${id}`, created_at: '2026-10-08T10:00:00Z', ...fields,
});
const question = (id: number) => comment(id, { comment_type: 'ask_user' });
const human = (id: number, replyTo?: number) => comment(id, { author_type: 'human', comment_type: '', reply_to_id: replyTo });

describe('pairQuestions', () => {
    it('gives a reply to the question it names, whatever the order', () => {
        const paired = pairQuestions([question(1), question(2), human(3, 2), human(4, 1)]);
        expect(paired.replies.get(1)?.id).toBe(4);
        expect(paired.replies.get(2)?.id).toBe(3);
        expect(paired.open).toEqual([]);
    });

    it('reads a plain comment as the answer to the oldest open question only, and only once', () => {
        const paired = pairQuestions([question(1), question(2), human(3)]);
        expect(paired.replies.get(1)?.id).toBe(3);
        expect(paired.replies.has(2)).toBe(false);
        expect(paired.open.map(q => q.id)).toEqual([2]);
    });

    it('does not let a comment written before a question answer it', () => {
        const paired = pairQuestions([human(1), question(2)]);
        expect(paired.open.map(q => q.id)).toEqual([2]);
        expect(paired.replyIds.size).toBe(0);
    });

    it('ignores status changes and agent comments', () => {
        const paired = pairQuestions([
            question(1),
            comment(2, { author_type: 'human', comment_type: 'status_change' }),
            comment(3, { author_type: 'agent' }),
        ]);
        expect(paired.open.map(q => q.id)).toEqual([1]);
    });

    it('keeps the first reply when a question is answered twice', () => {
        const paired = pairQuestions([question(1), human(2, 1), human(3, 1)]);
        expect(paired.replies.get(1)?.id).toBe(2);
        expect(paired.replyIds.has(3)).toBe(false);
    });
});
