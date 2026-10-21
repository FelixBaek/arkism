/** @typedef {{workspace_id:string, project_id:string, user_id?:string, agent_id?:string}} Scope */
/** @typedef {{id:string, revision:number, status:string, kind:string, text:string, summary?:string, scope?:Scope}} Record */
/** @typedef {{items:Record[], next_cursor:string, authority:'data_only_untrusted'}} MemoryPage */
/** @typedef {{status:number, code:string, message:string}} APIError */
/** @typedef {{snapshot:()=>{items:Record[], cursor:string, loading:boolean, error:string}, reset:(key?:string)=>void, load:(key:string)=>Promise<Record[]>, refresh:(key:string)=>Promise<Record[]>}} MemoryPager */
// Type definitions only. No global state or runtime side effects.
export {};
