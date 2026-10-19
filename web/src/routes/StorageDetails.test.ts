import { fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, expect, it } from 'vitest';
import { get } from 'svelte/store';
import { route } from '$lib/router';
import { consumer, declaration, image, volume } from '$lib/storage/fixtures';
import { installFakeFetch, installFakeStream, type FakeFetch, type FakeStream } from '../vitest-setup';
import StorageImage from './StorageImage.svelte';
import StorageVolume from './StorageVolume.svelte';
let fake: FakeFetch; let stream: FakeStream | undefined;
afterEach(()=>{stream?.restore();stream=undefined;fake?.restore();});
it('renders only safe image details, consumers, declarations and escaped labels',async()=>{
 fake=installFakeFetch({'GET /api/storage/images/aaaaaaaaaaaa':{body:{...image({containers:[consumer],declaredBy:[declaration],digests:['redis@sha256:123'],size:1024}),architecture:'arm64',os:'linux',layers:7,labels:{hello:'<b>world</b>'},env:['SECRET=do-not-render'],cmd:'unsafe-cmd'}}});
 render(StorageImage,{id:'aaaaaaaaaaaa'}); await screen.findByRole('heading',{name:'redis:7',level:1});
 for(const text of ['redis@sha256:123','arm64 / linux','7','1.0 KiB','<b>world</b>']) expect(screen.getByText(text)).toBeTruthy();
 expect(screen.getByRole('button',{name:'Copy image ID'})).toBeTruthy(); expect(screen.getByRole('link',{name:'db'}).getAttribute('href')).toBe('/containers/db'); expect(screen.getByRole('link',{name:'media'}).getAttribute('href')).toBe('/stacks/media');
 expect(screen.getByRole('button',{name:'Delete'}).title).toBe('used by 1 container'); expect(document.body.textContent).not.toContain('do-not-render');expect(document.body.textContent).not.toContain('unsafe-cmd');expect(document.querySelector('b')).toBeNull();
});
it('renders volume fields and protection note without driver options',async()=>{
 fake=installFakeFetch({'GET /api/storage/volumes/media_db':{body:{...volume(),mountpoint:'/var/lib/docker/volumes/media_db',options:{password:'hidden-secret'}}}});
 render(StorageVolume,{name:'media_db'}); await screen.findByRole('heading',{name:'media_db',level:1});
 expect(screen.getByText('/var/lib/docker/volumes/media_db')).toBeTruthy();expect(screen.getAllByText('local')).toHaveLength(2);expect(screen.getAllByText(/declared by stack/).length).toBeGreaterThan(0);expect(screen.getByRole('button',{name:'Delete'}).getAttribute('aria-disabled')).toBe('true');expect(document.body.textContent).not.toContain('hidden-secret');
});
it.each(['image','volume'])('unknown %s has not found and matching back link',async kind=>{
 fake=installFakeFetch({}); if(kind==='image')render(StorageImage,{id:'aaaaaaaaaaaa'});else render(StorageVolume,{name:'missing'});
 expect(await screen.findByText(new RegExp(`${kind} not found`,'i'))).toBeTruthy();expect(screen.getByRole('link',{name:kind==='image'?'Back to images':'Back to volumes'}).getAttribute('href')).toBe(`/storage?tab=${kind}s`);
});
it('single volume deletion uses named plan and returns to volumes after operation ends',async()=>{
 const op={id:'op1',stack:'',action:'storage-cleanup',state:'running',startedAt:new Date().toISOString()};
 fake=installFakeFetch({'GET /api/storage/volumes/free':{body:{...volume({name:'free',protection:null,declaredBy:[]}),mountpoint:'/data'}},'POST /api/storage/plans':{body:{id:'p_1',kind:'volume',expiresAt:new Date(Date.now()+300000).toISOString(),createdAt:new Date().toISOString(),items:[{kind:'volume',id:'free',ref:'free',bytes:0}],protected:[],warnings:[],reclaim:{min:0,max:0,exact:true},confirmPhrase:'free'}},'POST /api/storage/plans/p_1/confirm':{body:op},'GET /api/operations/op1':{body:{...op,lines:[]}}});
 stream=installFakeStream({'/api/operations/op1/stream':[]});render(StorageVolume,{name:'free'});await screen.findByRole('heading',{name:'free',level:1});await fireEvent.click(screen.getByRole('button',{name:'Delete'}));
 const input=await screen.findByRole('textbox');await fireEvent.input(input,{target:{value:'free'}});await fireEvent.click(screen.getAllByRole('button',{name:'Delete'})[1]);await waitFor(()=>expect(stream!.requests).toHaveLength(1));
 expect(fake.calls.find(c=>c.path==='/api/storage/plans')?.body).toEqual({kind:'volume',name:'free'});stream.push('/api/operations/op1/stream',`event: done\ndata: ${JSON.stringify({...op,state:'succeeded'})}\n\n`);await waitFor(()=>expect(get(route).query.get('tab')).toBe('volumes'));
});
