import { describe, expect, it } from 'vitest';
import { canStopTask, isTaskRunning, readSmartExchange, rowQuery, waitNeedsAttention } from './workflow';

describe('rowQuery', () => {
    it('adds the row filter to the set of calls the panel is about', () => {
        expect(rowQuery('company_id=3&from=2026-10-01', 'model', 'model', 'strong/model')).toBe('company_id=3&from=2026-10-01&model=strong%2Fmodel');
    });

    it('replaces the panel task with one subtask exactly', () => {
        const query = new URLSearchParams(rowQuery('company_id=3&task_id=10&subtree=true', 'task', 'task_id', '12'));
        expect(query.getAll('task_id')).toEqual(['12']);
        expect(query.has('subtree')).toBe(false);
    });

    it('covers everything beneath a top-level task', () => {
        const query = new URLSearchParams(rowQuery('company_id=3', 'root_task', 'task_id', '10'));
        expect(query.get('task_id')).toBe('10');
        expect(query.get('subtree')).toBe('true');
    });
});

describe('readSmartExchange', () => {
    const prompt = JSON.stringify({
        messages: [{ role: 'system', content: 'You are the CTO agent.' }, { role: 'user', content: '## Task\nShip it' }],
        tools: [{ type: 'function', function: { name: 'ask_questions' } }, { type: 'function', function: { name: 'finish_refinement' } }],
    });

    it('pulls the prompt, the tools on offer and the chosen tool call out of the stored exchange', () => {
        const response = JSON.stringify({ choices: [{ message: { tool_calls: [{ function: { name: 'finish_refinement', arguments: '{"spec":"do x"}' } }] } }] });
        const view = readSmartExchange(prompt, response);
        expect(view.system).toBe('You are the CTO agent.');
        expect(view.user).toBe('## Task\nShip it');
        expect(view.tools).toEqual(['ask_questions', 'finish_refinement']);
        expect(view.answerTool).toBe('finish_refinement');
        expect(view.answerArguments).toBe('{\n  "spec": "do x"\n}');
    });

    it('keeps what it cannot parse readable instead of dropping it', () => {
        const view = readSmartExchange('plain prompt', 'provider said no');
        expect(view.user).toBe('plain prompt');
        expect(view.answerText).toBe('provider said no');
        expect(view.answerTool).toBe('');
    });
});

describe('task state helpers', () => {
    it('treats queued and working tasks as running, and parked ones as not', () => {
        expect(isTaskRunning('to-do')).toBe(true);
        expect(isTaskRunning('depends-on-task')).toBe(true);
        expect(isTaskRunning('in-progress')).toBe(true);
        expect(isTaskRunning('blocked')).toBe(false);
        expect(isTaskRunning('in-review')).toBe(false);
    });

    it('offers stop for anything working or waiting, but not for a task already stopped', () => {
        expect(canStopTask({ status: 'in-progress', waiting_on: 'run' })).toBe(true);
        expect(canStopTask({ status: 'blocked', waiting_on: 'human' })).toBe(true);
        expect(canStopTask({ status: 'blocked', waiting_on: 'operator' })).toBe(false);
        expect(canStopTask({ status: 'done', waiting_on: '' })).toBe(false);
    });

    it('flags the waits a person has to resolve', () => {
        expect(waitNeedsAttention('human')).toBe(true);
        expect(waitNeedsAttention('vault')).toBe(true);
        expect(waitNeedsAttention('config')).toBe(true);
        expect(waitNeedsAttention('subtasks')).toBe(false);
        expect(waitNeedsAttention('run')).toBe(false);
    });
});
