// WebAuthn (passkey) client helpers. Login is passwordless and the passkey's
// PRF output unlocks the per-user encryption key server-side, so every
// ceremony extracts the PRF result and sends it (base64url) in the finish body.
import axios from 'axios';

function b64urlToBuf(s: string): ArrayBuffer {
    const pad = s.length % 4 === 0 ? '' : '='.repeat(4 - (s.length % 4));
    const bin = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'));
    const arr = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i);
    return arr.buffer;
}

function bufToB64url(buf: ArrayBuffer): string {
    const bytes = new Uint8Array(buf);
    let s = '';
    for (const b of bytes) s += String.fromCharCode(b);
    return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

// isSupported reports whether this browser can do passkeys at all.
export function passkeysSupported(): boolean {
    return typeof window !== 'undefined' && !!window.PublicKeyCredential;
}

function prepCreateOptions(pk: any): PublicKeyCredentialCreationOptions {
    pk.challenge = b64urlToBuf(pk.challenge);
    pk.user.id = b64urlToBuf(pk.user.id);
    (pk.excludeCredentials || []).forEach((c: any) => { c.id = b64urlToBuf(c.id); });
    if (pk.extensions?.prf?.eval?.first) pk.extensions.prf.eval.first = b64urlToBuf(pk.extensions.prf.eval.first);
    return pk;
}

function prepGetOptions(pk: any): PublicKeyCredentialRequestOptions {
    pk.challenge = b64urlToBuf(pk.challenge);
    (pk.allowCredentials || []).forEach((c: any) => { c.id = b64urlToBuf(c.id); });
    if (pk.extensions?.prf?.eval?.first) pk.extensions.prf.eval.first = b64urlToBuf(pk.extensions.prf.eval.first);
    return pk;
}

function serializeCreate(cred: PublicKeyCredential): any {
    const r = cred.response as AuthenticatorAttestationResponse;
    return {
        id: cred.id,
        rawId: bufToB64url(cred.rawId),
        type: cred.type,
        response: {
            attestationObject: bufToB64url(r.attestationObject),
            clientDataJSON: bufToB64url(r.clientDataJSON),
        },
        clientExtensionResults: {},
    };
}

function serializeGet(cred: PublicKeyCredential): any {
    const r = cred.response as AuthenticatorAssertionResponse;
    return {
        id: cred.id,
        rawId: bufToB64url(cred.rawId),
        type: cred.type,
        response: {
            authenticatorData: bufToB64url(r.authenticatorData),
            clientDataJSON: bufToB64url(r.clientDataJSON),
            signature: bufToB64url(r.signature),
            userHandle: r.userHandle ? bufToB64url(r.userHandle) : null,
        },
        clientExtensionResults: {},
    };
}

function prfOutput(cred: PublicKeyCredential): string {
    const ext = cred.getClientExtensionResults() as any;
    const first = ext?.prf?.results?.first;
    return first ? bufToB64url(first as ArrayBuffer) : '';
}

// runCeremony drives begin → navigator.credentials → finish for a create or
// get ceremony, returning the finish response data.
async function runCeremony(beginURL: string, finishURL: string, beginBody: any, kind: 'create' | 'get') {
    const begin = await axios.post(beginURL, beginBody);
    const { session_id, publicKey } = begin.data;
    let cred: PublicKeyCredential;
    let payload: any;
    if (kind === 'create') {
        cred = (await navigator.credentials.create({ publicKey: prepCreateOptions(publicKey) })) as PublicKeyCredential;
        payload = serializeCreate(cred);
    } else {
        cred = (await navigator.credentials.get({ publicKey: prepGetOptions(publicKey) })) as PublicKeyCredential;
        payload = serializeGet(cred);
    }
    const finish = await axios.post(finishURL, {
        session_id,
        credential: payload,
        prf: prfOutput(cred),
    });
    return finish.data;
}

export function register(email: string, inviteToken?: string) {
    return runCeremony('/api/auth/register/begin', '/api/auth/register/finish',
        { email, invite_token: inviteToken || '' }, 'create');
}

export function login(email: string) {
    return runCeremony('/api/auth/login/begin', '/api/auth/login/finish', { email }, 'get');
}

// unlock re-warms the encryption vault after a crash (session already valid).
export function unlock() {
    return runCeremony('/api/auth/unlock/begin', '/api/auth/unlock/finish', {}, 'get');
}

// addDevice enrolls another passkey for the signed-in (and unlocked) user.
export function addDevice() {
    return runCeremony('/api/auth/credentials/begin', '/api/auth/credentials/finish', {}, 'create');
}
