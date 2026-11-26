/** @vitest-environment jsdom */
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { TangledRepositoryForm } from "./tangled-repository-form";
import { getCurrentUser } from "@/lib/auth";
import { fetchTangledRepositories } from "@/lib/atproto-projects-api";
import { connectRepo, type RepositoryMigrationSource } from "@/lib/projects-api";
vi.mock("@/lib/auth",()=>({getCurrentUser:vi.fn()}));
vi.mock("@/lib/atproto-projects-api",()=>({fetchTangledRepositories:vi.fn()}));
vi.mock("@/lib/projects-api",()=>({connectRepo:vi.fn()}));
let host:HTMLDivElement,root:Root,client:QueryClient;const connected=vi.fn();
beforeEach(()=>{host=document.createElement("div");document.body.append(host);root=createRoot(host);client=new QueryClient({defaultOptions:{queries:{retry:false},mutations:{retry:false}}});});
afterEach(async()=>{await act(async()=>root.unmount());host.remove();client.clear();vi.resetAllMocks();});
async function settle(check:()=>void){let last:unknown;for(let i=0;i<50;i++){try{check();return;}catch(e){last=e;}await act(async()=>{await new Promise(r=>setTimeout(r,5));});}throw last;}
async function render(migrateFrom?: RepositoryMigrationSource, disabled = false){await act(async()=>root.render(<QueryClientProvider client={client}><TangledRepositoryForm projectId="project" onConnected={connected} migrateFrom={migrateFrom} disabled={disabled}/></QueryClientProvider>));}
async function input(id:string,value:string){const item=host.querySelector<HTMLInputElement>(`#${id}`)!;await act(async()=>{Object.getOwnPropertyDescriptor(HTMLInputElement.prototype,"value")!.set!.call(item,value);item.dispatchEvent(new Event("input",{bubbles:true}));});}
it("requires linked ATProto identity without attempting repository discovery",async()=>{vi.mocked(getCurrentUser).mockResolvedValue({id:"account",login:"owner"} as Awaited<ReturnType<typeof getCurrentUser>>);await render();await settle(()=>expect(host.textContent).toContain("Connect an ATProto identity"));expect(fetchTangledRepositories).not.toHaveBeenCalled();});
it("connects an exact private repository under its native owner DID",async()=>{vi.mocked(getCurrentUser).mockResolvedValue({id:"account",login:"owner",atproto_did:"did:plc:owner"} as Awaited<ReturnType<typeof getCurrentUser>>);vi.mocked(fetchTangledRepositories).mockResolvedValue({provider:"tangled",repositories:[],private_repository_selection:"exact"});vi.mocked(connectRepo).mockResolvedValue({provider:"tangled"} as Awaited<ReturnType<typeof connectRepo>>);await render();await settle(()=>expect(host.querySelector("#tangled-owner")).not.toBeNull());await input("tangled-owner","did:plc:owner");await input("tangled-name","private-skills");await act(async()=>host.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());await act(async()=>host.querySelector("form")!.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true})));await settle(()=>expect(connectRepo).toHaveBeenCalledWith("project",{provider:"tangled",owner:"did:plc:owner",repo:"private-skills",branch:"main",is_private:true}));expect(connected).toHaveBeenCalled();});
it("populates exact native selection from public repository discovery",async()=>{vi.mocked(getCurrentUser).mockResolvedValue({id:"account",atproto_did:"did:plc:owner"} as Awaited<ReturnType<typeof getCurrentUser>>);vi.mocked(fetchTangledRepositories).mockResolvedValue({provider:"tangled",repositories:[{repository_did:"did:plc:repo",owner_did:"did:plc:owner",owner_handle:"samclemente.me",name:"public",default_branch:"trunk",uri:"at://repo",cid:"cid"}],private_repository_selection:"exact"});await render();await settle(()=>expect(host.querySelectorAll("option")).toHaveLength(2));await act(async()=>{const select=host.querySelector("select")!;select.value="did:plc:repo";select.dispatchEvent(new Event("change",{bubbles:true}));});expect(host.querySelectorAll("option")[1].textContent).toBe("samclemente.me/public");expect(host.querySelector<HTMLInputElement>("#tangled-owner")!.value).toBe("did:plc:owner");expect(host.querySelector<HTMLInputElement>("#tangled-branch")!.value).toBe("trunk");});

it("keeps migration disabled until confirmed and sends the exact reviewed GitHub source", async () => {
  const source = { provider: "github", repo_owner: "owner", repo_name: "skills", default_branch: "main" };
  vi.mocked(getCurrentUser).mockResolvedValue({ id: "account", login: "owner", atproto_did: "did:plc:owner" } as Awaited<ReturnType<typeof getCurrentUser>>);
  vi.mocked(fetchTangledRepositories).mockResolvedValue({ provider: "tangled", repositories: [], private_repository_selection: "exact" });
  vi.mocked(connectRepo).mockResolvedValue({ provider: "tangled" } as Awaited<ReturnType<typeof connectRepo>>);
  await render(source, true);
  await settle(() => expect(host.querySelector("#tangled-owner")).not.toBeNull());
  await input("tangled-owner", "did:plc:owner");
  await input("tangled-name", "copied");
  await act(async () => host.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
  expect(connectRepo).not.toHaveBeenCalled();
  await render(source, false);
  await act(async () => host.querySelector("form")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })));
  await settle(() => expect(connectRepo).toHaveBeenCalledWith("project", { provider: "tangled", owner: "did:plc:owner", repo: "copied", branch: "main", is_private: false, migrate_from: source }));
});

it("loads the next repository page with handles and a DID fallback", async () => {
  vi.mocked(getCurrentUser).mockResolvedValue({ id: "account", atproto_did: "did:plc:owner" } as Awaited<ReturnType<typeof getCurrentUser>>);
  vi.mocked(fetchTangledRepositories).mockResolvedValueOnce({ provider: "tangled", repositories: [{ repository_did: "did:plc:one", owner_did: "did:plc:owner", owner_handle: "samclemente.me", name: "first", default_branch: "main", uri: "at://one", cid: "cid" }], cursor: "page-two", private_repository_selection: "exact" }).mockResolvedValueOnce({ provider: "tangled", repositories: [{ repository_did: "did:plc:two", owner_did: "did:plc:unresolved", name: "second", default_branch: "main", uri: "at://two", cid: "cid" }], private_repository_selection: "exact" });
  await render();
  await settle(() => expect(host.textContent).toContain("samclemente.me/first"));
  const more = [...host.querySelectorAll("button")].find((button) => button.textContent === "Load more repositories")!;
  await act(async () => more.click());
  await settle(() => expect(host.querySelectorAll("option")).toHaveLength(3));
  expect(fetchTangledRepositories).toHaveBeenLastCalledWith("page-two");
  expect(host.textContent).toContain("did:plc:unresolved/second");
});
